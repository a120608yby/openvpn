# Azure 登录 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 打开 Azure 认证后，普通用户的 Web 登录和 OpenVPN 拨入改为用用户名密码向 Microsoft Entra ID 校验，用户属性从 Microsoft Graph 读取；管理员仍用本地账号。

**Architecture:** 新增 `azure.go`，职责对齐 `ldap.go`：应用令牌查用户，可选安全组，再用资源所有者密码向 Entra ID 验证口令。`User.Login` 在重复登录检查之后进入该分支。系统设置里 Azure 与 LDAP 互斥，客户端机密用现有 AES 存储。

**Tech Stack:** Go 1.25.4、Gin、Viper、`github.com/gavintan/gopkg/aes`、Microsoft Entra ID 令牌接口、Microsoft Graph。前端是嵌入的 HTML 与 jQuery。

## Global Constraints

- 设计全文：`docs/superpowers/specs/2026-10-13-azure-login-design.md`。
- 登录页不跳转 Microsoft，表单仍是用户名和密码。
- `POST /login` 中用户名等于管理员时只做本地 bcrypt，不把口令发往 Azure。
- 不创建本地 VPN 账号，不把 Azure 用户写入 `ovpn.db`，不写 `.ovc`。
- `build/openvpn-auth` 不改。
- Azure 与 LDAP 同时只能开一个；校验失败时不写配置、不关闭另一个开关。
- 客户端机密用 `system.base.secret_key` 做 AES，算法与邮件口令相同。
- 每次外呼超时 10 秒。日志不记录口令、客户端机密和应用令牌。
- 用户可见错误文案使用设计文档「错误」表中的原句。
- 仓库没有测试套件。不新增、不运行 `go test`。每个任务的验收是 `gofmt -l .` 无输出且 `go build ./...` 成功。
- 提交说明使用 `feat:`、`fix:`、`docs:`，后面用中文说明原因。不要提交 `config.json`、密钥或令牌。
- 改 `templates/` 后必须重新编译，嵌入发生在编译期。
- 不要重命名 `SysBeseConfig`、`upadteOvpnConfig`、`setOnlineClinetNft`。不要打开被注释的 `gin.Recovery()`。

---

### Task 1: Azure 配置结构

**Files:**
- Modify: `src/openvpn-web/config.go`

**Interfaces:**
- Consumes: 现有 `config`、`loadConfig`、`initConfig`
- Produces: `SysAzureConfig`；包级变量 `azureAuth`、`azureTenantID`、`azureClientID`、`azureClientSecret`、`azureUserAttribute`、`azureGroupFilter`、`azureGroupID`、`azureAttrIpaddr`、`azureAttrConfig`

- [ ] **Step 1: 增加结构体并挂到 config**

在 `SysLdapConfig` 之后增加：

```go
type SysAzureConfig struct {
	AzureAuth       bool   `json:"azure_auth" mapstructure:"azure_auth"`
	TenantID        string `json:"tenant_id" mapstructure:"tenant_id"`
	ClientID        string `json:"client_id" mapstructure:"client_id"`
	ClientSecret    string `json:"client_secret" mapstructure:"client_secret"`
	UserAttribute   string `json:"user_attribute" mapstructure:"user_attribute"`
	GroupFilter     bool   `json:"group_filter" mapstructure:"group_filter"`
	GroupID         string `json:"group_id" mapstructure:"group_id"`
	AttrIpaddr      string `json:"attr_ipaddr" mapstructure:"attr_ipaddr"`
	AttrConfig      string `json:"attr_config" mapstructure:"attr_config"`
}
```

在 `config.System` 中、`Ldap` 字段之后增加 `Azure SysAzureConfig`，json 与 mapstructure 都是 `azure`。

在 ldap 包级变量之后增加：

```go
azureAuth          bool
azureTenantID      string
azureClientID      string
azureClientSecret  string
azureUserAttribute string
azureGroupFilter   bool
azureGroupID       string
azureAttrIpaddr    string
azureAttrConfig    string
```

- [ ] **Step 2: 写入默认值**

在 `initConfig` 里 `system.ldap.ldap_bind_password` 默认值之后增加：

```go
viper.SetDefault("system.azure.azure_auth", false)
viper.SetDefault("system.azure.tenant_id", "")
viper.SetDefault("system.azure.client_id", "")
viper.SetDefault("system.azure.client_secret", "")
viper.SetDefault("system.azure.user_attribute", "userPrincipalName")
viper.SetDefault("system.azure.group_filter", false)
viper.SetDefault("system.azure.group_id", "")
viper.SetDefault("system.azure.attr_ipaddr", "onPremisesExtensionAttributes.extensionAttribute1")
viper.SetDefault("system.azure.attr_config", "onPremisesExtensionAttributes.extensionAttribute2")
```

- [ ] **Step 3: 在 loadConfig 中加载**

在 `ldapBindPassword = ...` 之后增加。此任务先把落盘值原样放入 `azureClientSecret`，Task 2 再改为解密后的明文。

```go
azureAuth = conf.System.Azure.AzureAuth
azureTenantID = conf.System.Azure.TenantID
azureClientID = conf.System.Azure.ClientID
azureClientSecret = conf.System.Azure.ClientSecret
azureUserAttribute = conf.System.Azure.UserAttribute
azureGroupFilter = conf.System.Azure.GroupFilter
azureGroupID = conf.System.Azure.GroupID
azureAttrIpaddr = conf.System.Azure.AttrIpaddr
azureAttrConfig = conf.System.Azure.AttrConfig
```

- [ ] **Step 4: 编译**

在 `src/openvpn-web` 执行：

```powershell
gofmt -w config.go
gofmt -l .
go build ./...
```

Expected: `gofmt -l` 无输出，`go build ./...` 退出码 0。

- [ ] **Step 5: 提交**

```powershell
git add src/openvpn-web/config.go
git commit -m "feat: 增加 Azure 认证配置项"
```

---

### Task 2: Azure 目录客户端

**Files:**
- Create: `src/openvpn-web/azure.go`
- Modify: `src/openvpn-web/config.go`（只改 `azureClientSecret` 那一行赋值）

**Interfaces:**
- Consumes: Task 1 的包级 Azure 变量、`ovData`、`viper`
- Produces: `plainAzureSecret(stored string) string`；`AzureUserData`；`AzureConn.Auth(clogin bool, username, password, commonName string) error`；`AzureConn.Get(username string) (AzureUserData, error)`

- [ ] **Step 1: 写 azure.go**

创建 `src/openvpn-web/azure.go`，内容如下。查找先于验密。组过滤未通过返回「用户不存在」。组接口返回 401 或 403，或其他非 200，返回「Azure 连接失败」加简短原因。

```go
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
```

- [ ] **Step 2: loadConfig 使用解密后的机密**

把 Task 1 中这一行：

```go
azureClientSecret = conf.System.Azure.ClientSecret
```

改为：

```go
azureClientSecret = plainAzureSecret(conf.System.Azure.ClientSecret)
```

- [ ] **Step 3: 编译**

在 `src/openvpn-web` 执行：

```powershell
gofmt -w azure.go config.go
gofmt -l .
go build ./...
```

Expected: `gofmt -l` 无输出，构建成功。

- [ ] **Step 4: 提交**

```powershell
git add src/openvpn-web/azure.go src/openvpn-web/config.go
git commit -m "feat: 用 Graph 和用户名密码校验 Azure 用户"
```

---

### Task 3: 登录走 Azure 分支

**Files:**
- Modify: `src/openvpn-web/user.go`（`Login` 里约 152 行的 `if ldapAuth`）

**Interfaces:**
- Consumes: `azureAuth`、`AzureConn.Auth`
- Produces: `User.Login` 在 `azureAuth` 为真时不再查询本地用户表

- [ ] **Step 1: 插入分支**

不要改 `Login` 前半段的重复登录检查，也不要改 `main.go` 里管理员 bcrypt 分支。把：

```go
if ldapAuth {
    l, err := InitLdap()
    if err != nil {
        return err
    }

    return l.Auth(clogin, user, pass, commonName)
} else {
```

改为：

```go
if azureAuth {
    return (&AzureConn{}).Auth(clogin, user, pass, commonName)
} else if ldapAuth {
    l, err := InitLdap()
    if err != nil {
        return err
    }

    return l.Auth(clogin, user, pass, commonName)
} else {
```

- [ ] **Step 2: 编译**

在 `src/openvpn-web` 执行：

```powershell
gofmt -w user.go
gofmt -l .
go build ./...
```

Expected: 构建成功。

- [ ] **Step 3: 提交**

```powershell
git add src/openvpn-web/user.go
git commit -m "feat: 普通用户登录和拨入在 Azure 开启时走 Entra ID"
```

---

### Task 4: 设置保存、互斥与机密解密

**Files:**
- Modify: `src/openvpn-web/main.go`（`GET /settings` 约 706 行，`POST /settings` 约 713 行）

**Interfaces:**
- Consumes: `plainAzureSecret`、`aes.AesEncrypt`、`SysAzureConfig`
- Produces: 保存 Azure 开关、组过滤、用户属性和客户端机密的校验；`GET /settings` 返回解密后的 `client_secret`

- [ ] **Step 1: GET 返回明文机密**

`GET /settings` 在 `viper.Unmarshal(&conf)` 之后、`c.JSON` 之前增加：

```go
conf.System.Azure.ClientSecret = plainAzureSecret(conf.System.Azure.ClientSecret)
```

- [ ] **Step 2: POST 校验、加密和互斥**

在 `POST /settings` 的 `for` 循环之前增加 `message := "更新成功"`。循环内的 `switch k` 增加这些分支，并且校验失败时直接 `return`，此时还没有 `WriteConfig`：

```go
case "system.azure.client_secret":
    encrypted, err := aes.AesEncrypt(val, secretKey)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
        return
    }
    val = encrypted
case "system.azure.user_attribute":
    if val != "userPrincipalName" && val != "mail" {
        c.JSON(http.StatusInternalServerError, gin.H{"message": "用户属性只允许 userPrincipalName 或 mail"})
        return
    }
case "system.azure.azure_auth":
    if val == "true" {
        if viper.GetString("system.azure.tenant_id") == "" || viper.GetString("system.azure.client_id") == "" || viper.GetString("system.azure.client_secret") == "" {
            c.JSON(http.StatusInternalServerError, gin.H{"message": "请先填写租户 ID、应用程序 ID 和客户端机密"})
            return
        }
        if viper.GetBool("system.ldap.ldap_auth") {
            viper.Set("system.ldap.ldap_auth", false)
            message = "更新成功，已关闭 LDAP 认证"
        }
    }
case "system.azure.group_filter":
    if val == "true" && viper.GetString("system.azure.group_id") == "" {
        c.JSON(http.StatusInternalServerError, gin.H{"message": "请先填写组 ID"})
        return
    }
case "system.ldap.ldap_auth":
    if val == "true" && viper.GetBool("system.azure.azure_auth") {
        viper.Set("system.azure.azure_auth", false)
        message = "更新成功，已关闭 Azure 认证"
    }
```

把循环之后的成功响应从固定的「更新成功」改为 `gin.H{"message": message}`。

不要改管理员口令、邮件口令、子网和 DNS 的现有分支。

- [ ] **Step 3: 编译**

在 `src/openvpn-web` 执行：

```powershell
gofmt -w main.go
gofmt -l .
go build ./...
```

Expected: 构建成功。

- [ ] **Step 4: 提交**

```powershell
git add src/openvpn-web/main.go
git commit -m "feat: 保存 Azure 设置时加密机密并与 LDAP 互斥"
```

---

### Task 5: 用户页接口

**Files:**
- Modify: `src/openvpn-web/main.go` 的 `/client/userinfo`、`/client/modifyPass`、`/client/userConfig`、`/client/mfa`、`/client/mfa` 的 POST、`/client/mfa/:id` 的 DELETE

**Interfaces:**
- Consumes: `azureAuth`、`AzureConn.Get`
- Produces: Azure 用户能取信息和配置名；改密码与 MFA 接口返回设计文档中的原句

- [ ] **Step 1: userinfo 与 userConfig**

在 `/client/userinfo` 里，现有 `if ldapAuth` 之前增加。成功时 `return`，不再落到本地 `u.Info()`：

```go
if azureAuth {
    au, err := (&AzureConn{}).Get(u.Username)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
        return
    }
    c.JSON(http.StatusOK, au)
    return
}
```

在 `/client/userConfig` 里，现有 `if ldapAuth` 之前增加。只替换 `configName`，后面的空配置名检查保持不变：

```go
if azureAuth {
    au, err := (&AzureConn{}).Get(u.Username)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
        return
    }
    configName = au.OvpnConfig
}
```

- [ ] **Step 2: 拒绝改密码和 MFA**

在 `POST /client/modifyPass` 绑定参数之后、会话校验之前：

```go
if azureAuth {
    c.JSON(http.StatusInternalServerError, gin.H{"message": "Azure用户不支持修改密码"})
    return
}
```

在 `GET /client/mfa` 现有 LDAP 判断之后：

```go
if azureAuth {
    c.JSON(http.StatusInternalServerError, gin.H{"message": "Azure用户不支持设置MFA"})
    return
}
```

`POST /client/mfa` 和 `DELETE /client/mfa/:id` 同样在处理本地用户之前加上这段 `azureAuth` 判断，文案同为「Azure用户不支持设置MFA」。

- [ ] **Step 3: 编译**

在 `src/openvpn-web` 执行：

```powershell
gofmt -w main.go
gofmt -l .
go build ./...
```

Expected: 构建成功。

- [ ] **Step 4: 提交**

```powershell
git add src/openvpn-web/main.go
git commit -m "feat: Azure 用户可下载配置但不能在本系统改密码或设置 MFA"
```

---

### Task 6: 设置页

**Files:**
- Modify: `src/openvpn-web/templates/index.html`（LDAP 配置块结束、邮件配置 `h6` 之前，约 1520 行）
- Modify: `src/openvpn-web/templates/static/js/settings.js`

**Interfaces:**
- Consumes: `GET/POST /settings` 的 `system.azure.*` 与 Task 4 的错误、互斥提示
- Produces: 设置页可编辑 Azure 配置；打开其中一个开关时取消另一个的勾选

- [ ] **Step 1: 增加表单**

在 LDAP 配置最外层 `div.mx-5` 结束之后、`<h6 class="mb-3 fw-bold">邮件配置</h6>` 之前插入：

```html
<div class="mx-5 my-3">
  <h6 class="mb-3 fw-bold">Azure配置</h6>
  <div class="ms-5">
    <div class="row mb-1 align-items-baseline">
      <label class="col-sm-3 col-form-label">Azure</label>
      <div class="col-sm-9">
        <div class="form-check">
          <input class="form-check-input" type="checkbox" value="" id="azureAuth" />
          <label class="form-check-label" for="azureAuth"> 启用 </label>
        </div>
      </div>
    </div>
    <div class="row mb-1">
      <label class="col-sm-3 col-form-label">租户 ID</label>
      <div class="col-sm-9">
        <input type="text" class="form-control form-control-sm border-0 border-bottom" id="azureTenantId" />
      </div>
    </div>
    <div class="row mb-1">
      <label class="col-sm-3 col-form-label">应用程序 ID</label>
      <div class="col-sm-9">
        <input type="text" class="form-control form-control-sm border-0 border-bottom" id="azureClientId" />
      </div>
    </div>
    <div class="row mb-1">
      <label class="col-sm-3 col-form-label">客户端机密</label>
      <div class="col-sm-9">
        <input type="password" class="form-control form-control-sm border-0 border-bottom" id="azureClientSecret" />
      </div>
    </div>
    <div class="row mb-1">
      <label class="col-sm-3 col-form-label">用户属性</label>
      <div class="col-sm-9">
        <input type="text" class="form-control form-control-sm border-0 border-bottom" id="azureUserAttribute" />
      </div>
    </div>
    <div class="row mb-1 align-items-baseline">
      <label class="col-sm-3 col-form-label">组过滤</label>
      <div class="col-sm-9">
        <div class="form-check">
          <input class="form-check-input" type="checkbox" value="" id="azureGroupFilter" />
          <label class="form-check-label" for="azureGroupFilter"> 启用 </label>
        </div>
      </div>
    </div>
    <div class="row mb-1">
      <label class="col-sm-3 col-form-label">组 ID</label>
      <div class="col-sm-9">
        <input type="text" class="form-control form-control-sm border-0 border-bottom" id="azureGroupId" />
      </div>
    </div>
    <div class="row mb-1">
      <label class="col-sm-3 col-form-label">IP 属性</label>
      <div class="col-sm-9">
        <input type="text" class="form-control form-control-sm border-0 border-bottom" id="azureAttrIpaddr" />
      </div>
    </div>
    <div class="row">
      <label class="col-sm-3 col-form-label">配置属性</label>
      <div class="col-sm-9">
        <input type="text" class="form-control form-control-sm border-0 border-bottom" id="azureAttrConfig" />
      </div>
    </div>
  </div>
</div>
```

- [ ] **Step 2: 填充与保存**

在 `settings.js` 填充 LDAP 字段的语句之后增加：

```javascript
$('#azureAuth').prop('checked', data.system.azure?.azure_auth);
$('#azureTenantId').val(data.system.azure?.tenant_id);
$('#azureClientId').val(data.system.azure?.client_id);
$('#azureClientSecret').val(data.system.azure?.client_secret);
$('#azureUserAttribute').val(data.system.azure?.user_attribute);
$('#azureGroupFilter').prop('checked', data.system.azure?.group_filter);
$('#azureGroupId').val(data.system.azure?.group_id);
$('#azureAttrIpaddr').val(data.system.azure?.attr_ipaddr);
$('#azureAttrConfig').val(data.system.azure?.attr_config);
```

把现有 `#ldapAuth` 的 change 处理换成下面这段，并在 LDAP 段落后增加 Azure 段。保存失败时 `request.post` 会抛错，`.catch` 把复选框拨回点击前的状态：

```javascript
$(document).on('change', '#ldapAuth', function () {
  const box = $(this);
  const enabled = box.prop('checked');
  request
    .post('/settings', { 'system.ldap.ldap_auth': enabled })
    .then((data) => {
      if (enabled) {
        $('#azureAuth').prop('checked', false);
      }
      message.success(data.message);
    })
    .catch(() => {
      box.prop('checked', !enabled);
    });
});

$(document).on('change', '#azureAuth', function () {
  const box = $(this);
  const enabled = box.prop('checked');
  request
    .post('/settings', { 'system.azure.azure_auth': enabled })
    .then((data) => {
      if (enabled) {
        $('#ldapAuth').prop('checked', false);
      }
      message.success(data.message);
    })
    .catch(() => {
      box.prop('checked', !enabled);
    });
});

function bindAzureText(selector, key) {
  $(document).on('focus', selector, function () {
    $(this).data('oldValue', $(this).val());
  });
  $(document).on('blur', selector, function () {
    const oldValue = $(this).data('oldValue');
    const newValue = $(this).val();
    if (oldValue === newValue) return;
    const el = $(this);
    request
      .post('/settings', { [key]: newValue })
      .then((data) => {
        message.success(data.message);
      })
      .catch(() => {
        el.val(oldValue);
      });
  });
}

bindAzureText('#azureTenantId', 'system.azure.tenant_id');
bindAzureText('#azureClientId', 'system.azure.client_id');
bindAzureText('#azureClientSecret', 'system.azure.client_secret');
bindAzureText('#azureUserAttribute', 'system.azure.user_attribute');
bindAzureText('#azureGroupId', 'system.azure.group_id');
bindAzureText('#azureAttrIpaddr', 'system.azure.attr_ipaddr');
bindAzureText('#azureAttrConfig', 'system.azure.attr_config');

$(document).on('change', '#azureGroupFilter', function () {
  const box = $(this);
  const enabled = box.prop('checked');
  request
    .post('/settings', { 'system.azure.group_filter': enabled })
    .then((data) => {
      message.success(data.message);
    })
    .catch(() => {
      box.prop('checked', !enabled);
    });
});
```

- [ ] **Step 3: 编译**

在 `src/openvpn-web` 执行：

```powershell
gofmt -l .
go build ./...
```

Expected: 构建成功。模板由 `go:embed` 打进二进制。

- [ ] **Step 4: 提交**

```powershell
git add src/openvpn-web/templates/index.html src/openvpn-web/templates/static/js/settings.js
git commit -m "feat: 系统设置增加 Azure 认证表单"
```

---

### Task 7: 账号页、防火墙和用户页提示

**Files:**
- Modify: `src/openvpn-web/templates/static/js/index.js`（约 292 行 LDAP 提示）
- Modify: `src/openvpn-web/templates/static/js/firewall.js`（约 388 行）
- Modify: `src/openvpn-web/templates/client.html`（约 355 行）

**Interfaces:**
- Consumes: `data.system.azure.azure_auth`、`data.azureAuth`
- Produces: 管理端提示、防火墙用户组禁用、用户页禁止改密码

- [ ] **Step 1: 三处界面**

`index.js` 里现有 LDAP 判断改为先看 Azure：

```javascript
if (data.system.azure?.azure_auth) {
  const toast = $('#alertToast');
  toast.find('.toast-body').text('已启用Azure认证，本地VPN账号将不再用于登录！');
  bootstrap.Toast.getOrCreateInstance(toast).show();
} else if (data.system.ldap.ldap_auth) {
  const toast = $('#alertToast');
  toast.find('.toast-body').text('已启用LDAP认证，本地VPN账号将不在工作！');
  bootstrap.Toast.getOrCreateInstance(toast).show();
}
```

`firewall.js` 的判断改为：

```javascript
if (data.system.ldap.ldap_auth || data.system.azure?.azure_auth) {
  $('.firewall-tree[data-name="sug"]').addClass('disabled');
} else {
  $('.firewall-tree[data-name="sug"]').removeClass('disabled');
}
```

`client.html` 在 `if (data.ldapAuth)` 之前增加：

```javascript
if (data.azureAuth) {
  message.error('Azure用户不支持修改密码');
  return;
}
```

- [ ] **Step 2: 编译**

在 `src/openvpn-web` 执行：

```powershell
gofmt -l .
go build ./...
```

Expected: 构建成功。

- [ ] **Step 3: 提交**

```powershell
git add src/openvpn-web/templates/static/js/index.js src/openvpn-web/templates/static/js/firewall.js src/openvpn-web/templates/client.html
git commit -m "feat: Azure 开启时提示本地账号停用并禁用相关用户操作"
```

---

### Task 8: 文档

**Files:**
- Modify: `README.md`
- Modify: `AGENTS.md`
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: 前面任务实现的行为
- Produces: 三份文档各自写全 Azure 认证说明，正文不互相引用

- [ ] **Step 1: README**

在 `README.md` 的 LDAP 小节之后、文档维护之前增加：

```markdown
## Azure

在系统设置里启用 Azure 认证后，本地 VPN 账号不再用于 Web 登录和客户端拨入。管理员仍使用本地账号。Azure 与 LDAP 同时只能开启一个，打开其中一个会关闭另一个。

在 Microsoft Entra ID 注册单租户应用：创建客户端机密，授予应用程序权限 `User.Read.All` 并完成管理员同意。使用组过滤时再授予 `GroupMember.Read.All`。身份验证中打开「允许公共客户端流」。不需要重定向 URI。

登录名默认匹配 `userPrincipalName`，也可改为 `mail`。固定 IP 与客户端配置文件名写在用户的扩展属性上，默认分别是 `onPremisesExtensionAttributes.extensionAttribute1` 和 `extensionAttribute2`。启用了 MFA 或条件访问的账号无法用用户名密码登录。
```

- [ ] **Step 2: AGENTS.md**

在「必须保持的行为」中 LDAP 那条附近增加三条：

- 打开 `system.azure.azure_auth` 后，普通用户的 Web 登录和 `/ovpn/login` 走 `azure.go`，不再使用本地 VPN 账号。管理员登录仍在 `POST /login` 里做 bcrypt。
- Azure 与 LDAP 互斥。客户端机密以 AES 存在 `system.azure.client_secret`。`GET /settings` 返回解密后的明文。
- Entra ID 要求 MFA 或交互式条件访问时，用户名密码登录失败，文案与 `docs/superpowers/specs/2026-10-13-azure-login-design.md` 的错误表一致。实现以该设计文档为准。

在代码地图表增加一行：`azure.go` 为 Entra ID 用户查找、组过滤和口令校验。

- [ ] **Step 3: CLAUDE.md**

在「架构」中 LDAP 那条之后增加：

- `system.azure.azure_auth` 为真时，普通用户登录和拨入走 `azure.go`（Graph 查用户，令牌接口验口令）。与 LDAP 互斥。管理员仍是本地账号。客户端机密 AES 存储。MFA 或条件访问账号不能用用户名密码登录。

在「目录」中 `ldap` 的相邻位置写上 `azure.go` 的同一职责。不要把多步门户操作写进本文件，门户步骤留在 `README.md`。

- [ ] **Step 4: 提交**

```powershell
git add README.md AGENTS.md CLAUDE.md
git commit -m "docs: 说明 Azure 认证与 LDAP 互斥及 MFA 限制"
```

---

## 手动验收

设计文档要求的手动项不在本仓库自动化。实现全部任务后，用一份测试租户确认：

- 管理员本地账号进入 `/admin`。抓包或日志中不出现该口令发往 `login.microsoftonline.com`。
- 已填写租户、应用和机密后打开 Azure，LDAP 复选框取消，提示「更新成功，已关闭 LDAP 认证」。本地 VPN 账号登录失败。
- 三项未填时打开 Azure，保存失败，LDAP 仍为原状态，Azure 复选框回到未选。
- Azure 用户进入用户页，按扩展属性中的配置名下载文件。改密码提示「Azure用户不支持修改密码」。MFA 接口提示「Azure用户不支持设置MFA」。
- 错误口令返回「密码错误」。拨入同一账号时，IP 属性非空则 `OVPN_DATA/.ovip` 被写入。
