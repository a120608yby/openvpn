package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/gavintan/gopkg/aes"
	"github.com/spf13/viper"
)

type AzureUserData struct {
	Username   string `json:"username"`
	AzureAuth  bool   `json:"azureAuth"`
	Ipaddr     string `json:"ipaddr"`
	OvpnConfig string `json:"ovpnConfig"`
}

type AzureConn struct{}

func plainAzureSecret(stored string) string {
	if stored == "" {
		return ""
	}
	plain, err := aes.AesDecrypt(stored, secretKey)
	if err != nil {
		return ""
	}
	return plain
}

func (a *AzureConn) Auth(clogin bool, username, password, commonName string) error {
	user, err := a.lookup(username)
	if err != nil {
		return err
	}
	if err = a.checkGroup(user); err != nil {
		return err
	}
	if err = a.verifyPassword(username, password); err != nil {
		return err
	}
	if !clogin {
		return nil
	}

	configName := graphValue(user, azureAttrConfig)
	if viper.GetBool("system.base.validate_client_config") {
		if commonName != strings.TrimSuffix(configName, ".ovpn") {
			return fmt.Errorf("使用非法配置文件登录")
		}
	}
	ipaddr := graphValue(user, azureAttrIpaddr)
	if ipaddr != "" {
		os.WriteFile(path.Join(ovData, ".ovip"), []byte(ipaddr), 0644)
	}
	return nil
}

func (a *AzureConn) Get(username string) (AzureUserData, error) {
	user, err := a.lookup(username)
	if err != nil {
		return AzureUserData{}, err
	}
	if err = a.checkGroup(user); err != nil {
		return AzureUserData{}, err
	}
	return AzureUserData{
		Username:   username,
		AzureAuth:  true,
		Ipaddr:     graphValue(user, azureAttrIpaddr),
		OvpnConfig: graphValue(user, azureAttrConfig),
	}, nil
}

func (a *AzureConn) lookup(username string) (map[string]any, error) {
	token, err := a.appToken()
	if err != nil {
		return nil, err
	}
	attr := azureUserAttribute
	if attr != "mail" {
		attr = "userPrincipalName"
	}
	filter := fmt.Sprintf("%s eq '%s'", attr, strings.ReplaceAll(username, "'", "''"))
	endpoint := "https://graph.microsoft.com/v1.0/users?$filter=" + url.QueryEscape(filter) + "&$select=" + url.QueryEscape(graphSelect())
	body, status, err := a.getJSON(endpoint, token)
	if err != nil {
		return nil, fmt.Errorf("Azure 连接失败：%s", err.Error())
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("Azure 连接失败：%s", shortReason(body))
	}
	var result struct {
		Value []map[string]any `json:"value"`
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("Azure 连接失败：%s", err.Error())
	}
	if len(result.Value) == 0 {
		return nil, fmt.Errorf("用户不存在")
	}
	if len(result.Value) > 1 {
		return nil, fmt.Errorf("用户存在多个条目")
	}
	return result.Value[0], nil
}

func (a *AzureConn) checkGroup(user map[string]any) error {
	if !azureGroupFilter {
		return nil
	}
	id, _ := user["id"].(string)
	if id == "" || azureGroupID == "" {
		return fmt.Errorf("用户不存在")
	}
	token, err := a.appToken()
	if err != nil {
		return err
	}
	endpoint := "https://graph.microsoft.com/v1.0/users/" + url.PathEscape(id) + "/checkMemberGroups"
	body, status, err := a.postJSON(endpoint, token, map[string][]string{"groupIds": {azureGroupID}})
	if err != nil {
		return fmt.Errorf("Azure 连接失败：%s", err.Error())
	}
	if status != http.StatusOK {
		return fmt.Errorf("Azure 连接失败：%s", shortReason(body))
	}
	var resp struct {
		Value []string `json:"value"`
	}
	if err = json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("Azure 连接失败：%s", err.Error())
	}
	for _, g := range resp.Value {
		if strings.EqualFold(g, azureGroupID) {
			return nil
		}
	}
	return fmt.Errorf("用户不存在")
}

func (a *AzureConn) verifyPassword(username, password string) error {
	form := url.Values{}
	form.Set("client_id", azureClientID)
	form.Set("client_secret", azureClientSecret)
	form.Set("grant_type", "password")
	form.Set("username", username)
	form.Set("password", password)
	form.Set("scope", "openid")
	body, status, err := a.postForm(tokenEndpoint(), form)
	if err != nil {
		return fmt.Errorf("Azure 连接失败：%s", err.Error())
	}
	if status == http.StatusOK {
		return nil
	}
	return mapTokenError(body)
}

func (a *AzureConn) appToken() (string, error) {
	if azureTenantID == "" || azureClientID == "" || azureClientSecret == "" {
		return "", fmt.Errorf("Azure 连接失败：缺少租户 ID、应用程序 ID 或客户端机密")
	}
	form := url.Values{}
	form.Set("client_id", azureClientID)
	form.Set("client_secret", azureClientSecret)
	form.Set("grant_type", "client_credentials")
	form.Set("scope", "https://graph.microsoft.com/.default")
	body, status, err := a.postForm(tokenEndpoint(), form)
	if err != nil {
		return "", fmt.Errorf("Azure 连接失败：%s", err.Error())
	}
	if status != http.StatusOK {
		return "", mapTokenError(body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err = json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("Azure 连接失败：未取得应用令牌")
	}
	return tok.AccessToken, nil
}

func tokenEndpoint() string {
	return "https://login.microsoftonline.com/" + url.PathEscape(azureTenantID) + "/oauth2/v2.0/token"
}

func graphSelect() string {
	fields := []string{"id", "userPrincipalName", "mail"}
	for _, dotted := range []string{azureAttrIpaddr, azureAttrConfig} {
		top := dotted
		if i := strings.IndexByte(dotted, '.'); i > 0 {
			top = dotted[:i]
		}
		if top != "" {
			fields = append(fields, top)
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return strings.Join(out, ",")
}

func graphValue(user map[string]any, dotted string) string {
	if dotted == "" {
		return ""
	}
	var cur any = user
	for _, part := range strings.Split(dotted, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[part]
	}
	switch v := cur.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func mapTokenError(body []byte) error {
	var te struct {
		ErrorDescription string `json:"error_description"`
	}
	json.Unmarshal(body, &te)
	desc := te.ErrorDescription
	switch {
	case strings.Contains(desc, "AADSTS50126"):
		return fmt.Errorf("密码错误")
	case strings.Contains(desc, "AADSTS50076"), strings.Contains(desc, "AADSTS50079"):
		return fmt.Errorf("该账号启用了 MFA，无法用用户名密码登录")
	case strings.Contains(desc, "AADSTS50058"), strings.Contains(desc, "AADSTS50158"), strings.Contains(desc, "AADSTS53003"):
		return fmt.Errorf("该账号需要交互式登录（条件访问），无法用用户名密码登录")
	case strings.Contains(desc, "AADSTS7000218"):
		return fmt.Errorf("Azure 连接失败：应用未开启允许公共客户端流")
	default:
		return fmt.Errorf("Azure 连接失败：%s", shortReason(body))
	}
}

func shortReason(body []byte) string {
	var te struct {
		ErrorDescription string `json:"error_description"`
		Error            string `json:"error"`
	}
	json.Unmarshal(body, &te)
	reason := strings.TrimSpace(te.ErrorDescription)
	if reason == "" {
		reason = strings.TrimSpace(te.Error)
	}
	if reason == "" {
		reason = strings.TrimSpace(string(body))
	}
	if len(reason) > 180 {
		reason = reason[:180]
	}
	return reason
}

func azureHTTP() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

func (a *AzureConn) postForm(endpoint string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return a.do(req)
}

func (a *AzureConn) postJSON(endpoint, token string, payload any) ([]byte, int, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return a.do(req)
}

func (a *AzureConn) getJSON(endpoint, token string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return a.do(req)
}

func (a *AzureConn) do(req *http.Request) ([]byte, int, error) {
	resp, err := azureHTTP().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}
