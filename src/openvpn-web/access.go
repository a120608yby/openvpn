package main

import (
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"gorm.io/gorm"
)

// OpenVPN 2.6 移除了内置包过滤，不能再靠 minimal_pf.so 和 pf 文件做按用户过滤。
// 这里用 nftables 的 access 链实现同一套语义：显式 +/- 规则优先，然后是客户端互访，最后是网段默认策略。
// 放行使用 return，后面的防火墙规则仍可拒绝；拒绝使用 drop。

const accessRuleLimit = 64

var nftHandleRe = regexp.MustCompile(`handle (\d+)`)

type accessRule struct {
	Allow bool
	CIDR  string
	V6    bool
}

type accessPolicy struct {
	UserID  uint
	Clients string
	Subnets string
	Rules   []accessRule
}

func normalizeAccessPolicy(v string) (string, error) {
	switch strings.TrimSpace(v) {
	case "", "ACCEPT", "DROP":
		return strings.TrimSpace(v), nil
	default:
		return "", fmt.Errorf("策略只能是允许或拒绝")
	}
}

func normalizeAccessRules(text string) (string, error) {
	var lines []string
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(lines) >= accessRuleLimit {
			return "", fmt.Errorf("网段规则最多 %d 条", accessRuleLimit)
		}

		sign := line[0]
		if sign != '+' && sign != '-' {
			return "", fmt.Errorf("第 %d 行格式错误，应为 +网段 或 -网段", i+1)
		}

		cidr, v6, err := canonicalCIDR(line[1:])
		if err != nil {
			return "", fmt.Errorf("第 %d 行不是合法的 IP 或网段", i+1)
		}
		_ = v6
		lines = append(lines, string(sign)+cidr)
	}

	return strings.Join(lines, "\n"), nil
}

func canonicalCIDR(s string) (string, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false, fmt.Errorf("空地址")
	}

	if !strings.Contains(s, "/") {
		ip := net.ParseIP(s)
		if ip == nil {
			return "", false, fmt.Errorf("非法地址")
		}
		if v4 := ip.To4(); v4 != nil {
			return v4.String() + "/32", false, nil
		}
		return ip.String() + "/128", true, nil
	}

	ip, network, err := net.ParseCIDR(s)
	if err != nil {
		return "", false, err
	}
	if ip.To4() != nil {
		return network.String(), false, nil
	}
	return network.String(), true, nil
}

func canonicalIP(s string) (string, bool, bool) {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return "", false, false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String(), false, true
	}
	return ip.String(), true, true
}

func parseAccessRuleLines(text string) ([]accessRule, error) {
	normalized, err := normalizeAccessRules(text)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return nil, nil
	}

	var rules []accessRule
	for _, line := range strings.Split(normalized, "\n") {
		_, v6, err := canonicalCIDR(line[1:])
		if err != nil {
			return nil, err
		}
		rules = append(rules, accessRule{
			Allow: line[0] == '+',
			CIDR:  line[1:],
			V6:    v6,
		})
	}
	return rules, nil
}

func resolveAccessPolicy(username string) (accessPolicy, bool, error) {
	var user User
	if err := db.Select("id", "gid").First(&user, "username = ?", username).Error; err != nil {
		return accessPolicy{}, false, nil
	}

	type row struct {
		AccessClients *string
		AccessSubnets *string
		AccessRules   *string
	}
	var rows []row
	err := db.Raw(`
		WITH RECURSIVE group_up AS (
			SELECT id, parent_id, access_clients, access_subnets, access_rules, 0 AS level
			FROM "group"
			WHERE id = ?

			UNION ALL

			SELECT g.id, g.parent_id, g.access_clients, g.access_subnets, g.access_rules, gu.level + 1
			FROM "group" g
			JOIN group_up gu ON g.id = gu.parent_id
		)
		SELECT access_clients, access_subnets, access_rules
		FROM group_up
		ORDER BY level ASC
	`, user.Gid).Scan(&rows).Error
	if err != nil {
		return accessPolicy{}, false, err
	}

	policy := accessPolicy{UserID: user.ID, Clients: "ACCEPT", Subnets: "ACCEPT"}
	clientsSet := false
	subnetsSet := false
	for _, row := range rows {
		if !clientsSet && row.AccessClients != nil && *row.AccessClients != "" {
			policy.Clients = *row.AccessClients
			clientsSet = true
		}
		if !subnetsSet && row.AccessSubnets != nil && *row.AccessSubnets != "" {
			policy.Subnets = *row.AccessSubnets
			subnetsSet = true
		}
		if row.AccessRules != nil && *row.AccessRules != "" {
			rules, err := parseAccessRuleLines(*row.AccessRules)
			if err != nil {
				return accessPolicy{}, false, err
			}
			policy.Rules = append(policy.Rules, rules...)
		}
	}

	return policy, true, nil
}

func vpnNetworks() (v4 *net.IPNet, v4Server net.IP, v6 *net.IPNet, v6Server net.IP) {
	if cfg, err := initOvpnConfig(); err == nil {
		if s := cfg.Get("server"); s != "" {
			parts := strings.Fields(s)
			if len(parts) >= 2 {
				ip := net.ParseIP(parts[0])
				mask := dottedMask(parts[1])
				if ip != nil && mask != nil {
					ip = ip.To4()
					network := &net.IPNet{IP: ip.Mask(mask), Mask: mask}
					v4 = network
					server := append(net.IP(nil), network.IP.To4()...)
					incIP(server)
					if network.Contains(server) {
						v4Server = server
					}
				}
			}
		}
		if s := cfg.Get("server-ipv6"); s != "" {
			ip, network, err := net.ParseCIDR(strings.Fields(s)[0])
			if err == nil {
				v6 = network
				v6Server = ip
			}
		}
	}

	if v4 == nil {
		if _, network, err := net.ParseCIDR(viper.GetString("openvpn.ovpn_subnet")); err == nil {
			v4 = network
			server := append(net.IP(nil), network.IP.To4()...)
			incIP(server)
			if network.Contains(server) {
				v4Server = server
			}
		}
	}
	if v6 == nil {
		if ip, network, err := net.ParseCIDR(viper.GetString("openvpn.ovpn_subnet6")); err == nil {
			v6 = network
			v6Server = ip
		}
	}
	return v4, v4Server, v6, v6Server
}

func dottedMask(s string) net.IPMask {
	ip := net.ParseIP(s)
	if ip == nil {
		return nil
	}
	v4 := ip.To4()
	if v4 == nil {
		return nil
	}
	return net.IPMask(v4)
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

func accessRestricted(p accessPolicy) bool {
	return p.Clients == "DROP" || p.Subnets == "DROP" || len(p.Rules) > 0
}

type accessCmd struct {
	V6    bool
	SAddr string
	DAddr string
	Drop  bool
}

func buildAccessCmds(p accessPolicy, vip, vip6 string) ([]accessCmd, error) {
	v4net, v4srv, v6net, v6srv := vpnNetworks()
	var cmds []accessCmd

	addFamily := func(src string, v6 bool, network *net.IPNet, server net.IP) error {
		if src == "" {
			return nil
		}
		if accessRestricted(p) && (p.Clients == "DROP" || p.Subnets == "DROP") && network == nil {
			return fmt.Errorf("无法读取 VPN 网段，访问权限未生效")
		}

		for _, rule := range p.Rules {
			if rule.V6 != v6 {
				continue
			}
			cmds = append(cmds, accessCmd{V6: v6, SAddr: src, DAddr: rule.CIDR, Drop: !rule.Allow})
		}

		if p.Clients == "DROP" {
			if server != nil {
				cmds = append(cmds, accessCmd{V6: v6, SAddr: src, DAddr: server.String() + hostSuffix(v6), Drop: false})
			}
			if p.Subnets == "ACCEPT" && network != nil {
				cmds = append(cmds, accessCmd{V6: v6, SAddr: src, DAddr: network.String(), Drop: true})
			}
		} else if p.Subnets == "DROP" && network != nil {
			cmds = append(cmds, accessCmd{V6: v6, SAddr: src, DAddr: network.String(), Drop: false})
		}

		if p.Subnets == "DROP" {
			cmds = append(cmds, accessCmd{V6: v6, SAddr: src, Drop: true})
		}
		return nil
	}

	if err := addFamily(vip, false, v4net, v4srv); err != nil {
		return nil, err
	}
	if err := addFamily(vip6, true, v6net, v6srv); err != nil {
		return nil, err
	}
	return cmds, nil
}

func hostSuffix(v6 bool) string {
	if v6 {
		return "/128"
	}
	return "/32"
}

func runNft(args ...string) error {
	cmd := exec.Command("nft", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if len(out) == 0 {
			out = []byte(err.Error())
		}
		return fmt.Errorf("%s", out)
	}
	return nil
}

func runNftScript(script string) error {
	if strings.TrimSpace(script) == "" {
		return nil
	}
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		if len(out) == 0 {
			out = []byte(err.Error())
		}
		return fmt.Errorf("%s", out)
	}
	return nil
}

func chainExists(name string) bool {
	return exec.Command("nft", "list", "chain", "inet", nftTableName, name).Run() == nil
}

func chainLines(name string) ([]string, error) {
	cmd := exec.Command("nft", "-a", "list", "chain", "inet", nftTableName, name)
	out, err := cmd.Output()
	if err != nil {
		if len(out) == 0 {
			return nil, err
		}
		return nil, fmt.Errorf("%s", out)
	}
	return strings.Split(string(out), "\n"), nil
}

func handlesMatching(chain string, match func(string) bool) ([]string, error) {
	lines, err := chainLines(chain)
	if err != nil {
		return nil, err
	}
	var handles []string
	for _, line := range lines {
		if !match(line) {
			continue
		}
		m := nftHandleRe.FindStringSubmatch(line)
		if len(m) == 2 {
			handles = append(handles, m[1])
		}
	}
	return handles, nil
}

func ensureAccessChain() error {
	if !chainExists("access") {
		if err := runNft("add", "chain", "inet", nftTableName, "access"); err != nil {
			return err
		}
	}

	handles, err := handlesMatching("forward", func(line string) bool {
		return strings.Contains(line, `comment "access_jump"`)
	})
	if err != nil {
		return err
	}
	if len(handles) > 0 {
		return nil
	}

	for _, idx := range []string{"2", "1", "0"} {
		err = runNft("insert", "rule", "inet", nftTableName, "forward", "index", idx, "jump", "access", "comment", "access_jump")
		if err == nil {
			return nil
		}
	}
	return runNft("add", "rule", "inet", nftTableName, "forward", "jump", "access", "comment", "access_jump")
}

func deleteAccessBySource(ip string) error {
	if ip == "" || !chainExists("access") {
		return nil
	}
	handles, err := handlesMatching("access", func(line string) bool {
		return strings.Contains(line, "saddr "+ip+" ")
	})
	if err != nil {
		return err
	}
	var sb strings.Builder
	for _, handle := range handles {
		fmt.Fprintf(&sb, "delete rule inet %s access handle %s\n", nftTableName, handle)
	}
	return runNftScript(sb.String())
}

func clearAccessUser(id uint) error {
	if id == 0 || !chainExists("access") {
		return nil
	}
	prefix := fmt.Sprintf(`comment "acc_%d_`, id)
	handles, err := handlesMatching("access", func(line string) bool {
		return strings.Contains(line, prefix)
	})
	if err != nil {
		return err
	}
	if len(handles) == 0 {
		return nil
	}
	var sb strings.Builder
	for _, handle := range handles {
		fmt.Fprintf(&sb, "delete rule inet %s access handle %s\n", nftTableName, handle)
	}
	if err := runNftScript(sb.String()); err != nil {
		return err
	}
	return saveNftConfig()
}

func applyUserAccess(username, vip, vip6 string) error {
	v4, _, ok4 := canonicalIP(vip)
	v6, _, ok6 := canonicalIP(vip6)
	if ok4 {
		vip = v4
	} else {
		vip = ""
	}
	if ok6 {
		vip6 = v6
	} else {
		vip6 = ""
	}
	if vip == "" && vip6 == "" {
		return nil
	}

	policy, found, err := resolveAccessPolicy(username)
	if err != nil {
		return err
	}

	var cmds []accessCmd
	if found && accessRestricted(policy) {
		cmds, err = buildAccessCmds(policy, vip, vip6)
		if err != nil {
			return err
		}
		if err := ensureAccessChain(); err != nil {
			return err
		}
	}

	if err := deleteAccessBySource(vip); err != nil {
		return err
	}
	if err := deleteAccessBySource(vip6); err != nil {
		return err
	}
	if !found || !accessRestricted(policy) {
		return nil
	}

	var sb strings.Builder
	for _, cmd := range cmds {
		family := "ip"
		if cmd.V6 {
			family = "ip6"
		}
		verdict := "return"
		if cmd.Drop {
			verdict = "drop"
		}
		comment := fmt.Sprintf("acc_%d_%s", policy.UserID, cmd.SAddr)
		if cmd.DAddr == "" {
			fmt.Fprintf(&sb, "add rule inet %s access %s saddr %s %s comment \"%s\"\n", nftTableName, family, cmd.SAddr, verdict, comment)
			continue
		}
		fmt.Fprintf(&sb, "add rule inet %s access %s saddr %s %s daddr %s %s comment \"%s\"\n", nftTableName, family, cmd.SAddr, family, cmd.DAddr, verdict, comment)
	}
	return runNftScript(sb.String())
}

func clearUserAccessSession(username, vip, vip6 string) error {
	v4, _, ok4 := canonicalIP(vip)
	v6, _, ok6 := canonicalIP(vip6)
	if ok4 {
		if err := deleteAccessBySource(v4); err != nil {
			return err
		}
	}
	if ok6 {
		if err := deleteAccessBySource(v6); err != nil {
			return err
		}
	}
	_ = username
	return nil
}

func syncOnlineAccess(only map[string]struct{}) error {
	clients := (&ovpn{address: ovManage}).getClient()
	matched := false
	var errs []string
	for _, client := range clients {
		if client.Username == "" || client.Username == "UNDEF" {
			continue
		}
		if only != nil {
			if _, ok := only[client.Username]; !ok {
				continue
			}
		}
		matched = true
		if err := applyUserAccess(client.Username, client.Vip, client.Vip6); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	if matched {
		return saveNftConfig()
	}
	return nil
}

func syncGroupAccess(groupID string) error {
	var g Group
	users := g.GetUsers(groupID)
	only := make(map[string]struct{}, len(users))
	for _, user := range users {
		only[user.Username] = struct{}{}
	}
	return syncOnlineAccess(only)
}

func nullableColumn(v string) any {
	if v == "" {
		return gorm.Expr("NULL")
	}
	return v
}

func saveGroupAccess(g *Group, c *gin.Context) error {
	if g.ID == 0 {
		return fmt.Errorf("非法请求")
	}

	clients, err := normalizeAccessPolicy(c.PostForm("access_clients"))
	if err != nil {
		return fmt.Errorf("客户端互访%s", err.Error())
	}
	subnets, err := normalizeAccessPolicy(c.PostForm("access_subnets"))
	if err != nil {
		return fmt.Errorf("网段默认%s", err.Error())
	}
	rules, err := normalizeAccessRules(c.PostForm("access_rules"))
	if err != nil {
		return err
	}

	err = db.Model(&Group{ID: g.ID}).Updates(map[string]any{
		"access_clients": nullableColumn(clients),
		"access_subnets": nullableColumn(subnets),
		"access_rules":   nullableColumn(rules),
	}).Error
	g.AccessClients = nil
	g.AccessSubnets = nil
	g.AccessRules = nil
	return err
}
