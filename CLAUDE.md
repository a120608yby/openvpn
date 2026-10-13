# CLAUDE.md

OpenVPN 服务端与 Web 管理端。本文件在每次会话开始时加载，只写每次都用得上的事实，并且独立完整。

## 命令

在 `src/openvpn-web` 下工作。模块是 `openvpn-web`，`go 1.25.4`，`package main`。

```powershell
cd src/openvpn-web
go mod download
$env:OVPN_DATA = "D:\path\to\data"
New-Item -ItemType Directory -Force $env:OVPN_DATA
go run .
gofmt -w .
go build -o openvpn-web.exe .
gofmt -l .
go build ./...
```

`OVPN_DATA` 必填且可写。未设置或目录无效时，`init()` 里的 `initConfig()` 会 panic。没有单元测试，不要运行或报告 `go test`。

容器编排用仓库根目录的 `docker compose up -d`。`docker-compose.yml` 设置 `pull_policy: build`，构建上下文是仓库根目录，Dockerfile 是 `build/Dockerfile`，在本地构建 `yyxx/openvpn`，不拉取远程同名镜像。镜像编译 `src/openvpn-web`。Go 模块默认走腾讯云 `https://mirrors.cloud.tencent.com/go`，Alpine 软件源默认是 `mirrors.cloud.tencent.com`。发布工作流用 `GOPROXY=https://proxy.golang.org,direct` 和 `ALPINE_MIRROR=dl-cdn.alpinelinux.org`。Easy-RSA 仍从 GitHub Releases 下载。构建时去掉入口脚本和 `supervisord.conf` 的 CR，避免 Windows CRLF 检出导致容器启动失败。本地快速验证用 `go build`。根目录 `.dockerignore` 排除 `pkg/` 和 `data/`。

发布靠推送 git tag。`.github/workflows/build.yml` 用 GoReleaser 出二进制，再构建多架构镜像。

## 目录

- `src/openvpn-web/*.go`：管理端。路由在 `main.go` 的 `main()`。`azure.go` 负责 Entra ID 用户查找、组过滤和口令校验。
- `src/openvpn-web/templates/`：`//go:embed templates`。改 HTML、JS、CSS 后必须重新编译。
- `src/openvpn-web/templates/static/js/index.js` 动态导入 `utils.js`、`settings.js`、`user.js`、`client.js`、`cert.js`、`history.js`、`firewall.js`。文案在 `zh.json`。
- `src/openvpn-web/access.go`：分组访问权限，生成 nftables `access` 链规则。
- `build/docker-entrypoint.sh`：初始化 PKI 与 `server.conf`，并处理客户端连接脚本。
- `build/openvpn-auth`：`auth-user-pass-verify`，用 `O-Token` 调本机 `POST /ovpn/login`。
- `build/supervisord.conf`：同时拉起 `openvpn` 与 `openvpn-web`。
- `scripts/openvpn-install.sh`：裸机安装。

`OVPN_DATA`（容器内 `/data`）含 `config.json`、`ovpn.db`、`pki/`、`server.conf`、`ccd/`、`clients/`、`openvpn-status.log`。

## 架构

- Gin + 会话（cookie 名 `user_session`）+ GORM SQLite。启动时 `AutoMigrate`：`Group`、`User`、`History`、`Firewall`。
- `AuthMiddleWare()` 只保护在它之后注册的路由。`/login`、`/logout`、`/captcha` 是公开的。
- `/admin`、`/settings`、`/ovpn/*` 仅管理员。`/` 与 `/client/*` 给已登录 VPN 用户。
- `POST /ovpn/login`、`/ovpn/history`、`/ovpn/firewall` 允许无会话，条件是头 `O-Token` 等于 `system.base.token` 且来源为回环地址。
- 管理口默认 `127.0.0.1:7505`，由 `ovpn.sendCommand` 读取状态、踢客户端。
- 配置用 Viper 写 `config.json`。仅当 `system.base.auto_update_ovpn_config` 为真时，`upadteOvpnConfig()` 才改 `server.conf`。改完仍需手动重启 OpenVPN。
- LDAP 开启后，本地 VPN 账号不再用于认证。
- `system.azure.azure_auth` 为真时，普通用户登录和拨入走 `azure.go`（Graph 查用户，令牌接口验口令）。与 LDAP 互斥。管理员仍是本地账号。客户端机密 AES 存储。MFA 或条件访问账号不能用用户名密码登录。
- 防火墙规则落到 nftables，默认表名 `openvpn-nft`。分组访问权限另外写在同一张表的 `access` 链，字段是 `access_clients`、`access_subnets`、`access_rules`。LDAP 或 Azure 认证的用户如果本地没有同名 VPN 账号，不会套用这套规则。
- OpenVPN 2.6 没有内置包过滤。不要加 `minimal_pf.so`。访问权限在 `learn-address` 时随 `/ovpn/firewall?a=add_ovips` 下发，断开时删除。空策略表示继承上级，根上仍为空则全部放行。
- 入口脚本会注释 `client-to-client` 并打开 IP 转发，VPN 网段之间不走 MASQUERADE，客户端互访才会经过防火墙。

端口：Web `8833`，VPN `1194/udp`。

## 约定

- Go 代码 `gofmt`。领域逻辑放在已有文件，用 `Create` / `Update` / `Delete` / `All` / `Get`。新 HTTP 路由才加到 `main.go`。
- JSON 错误沿用 `gin.H{"message": "..."}`，用户可见文案用中文。
- 提交信息使用 `feat:`、`fix:`、`refactor:`、`chore:`、`docs:`，与 `.goreleaser.yml` 的分组一致，正文用中文说明原因。
- 前端用 `.prettierrc.yml`：2 空格、单引号、分号、行宽 120。
- 不要重命名 `SysBeseConfig`、`upadteOvpnConfig`、`setOnlineClinetNft`。
- 管理员口令 bcrypt。VPN 账号口令与邮件口令用 `system.base.secret_key` 做 AES。`User.BeforeSave` 会清洗字符串，但必须跳过 `Password`。
- 默认不推送 `redirect-gateway`。只有 `openvpn.ovpn_gateway` 为真才让客户端流量走 VPN。
- IPv6 需要系统设置、`udp6`/`tcp6`、Docker IPv6 三者同时开启。
- 证书默认 365 天。用 `EASYRSA_CA_EXPIRE`、`EASYRSA_CERT_EXPIRE`、`EASYRSA_CRL_DAYS` 覆盖。
- `gin.Recovery()` 保持注释状态，不要顺手打开。
- 入口脚本读 `.openvpn.ovpn_maxclients`，Go 字段是 `ovpn_max_clients`。改最大连接数时两边一起改。

## 不要做

- 不要提交 `config.json`、`ovpn.db`、`pki/`、私钥、token 或口令。
- 不要把管理口 `7505` 或 `/ovpn/login` 绑到非回环地址。
- 不要关掉会话、证书、`O-Token` 或回环校验来方便调试。
- 不要把多步操作流程写进本文件。流程放到 `.claude/skills/`，并在下面的维护规则里同步文档。

## 文档维护

每次 bug 修复、技能更新或新增后，同一次变更改完这三处，每处自己写全：

- 本文件：受影响的命令、目录、约定、限制
- `README.md`：使用者可见的功能、部署和注意事项
- 仓库根目录里另一份编码代理规范：同一批事实，按那份文件自己的章节写入

不改变行为、命令、路径或限制的纯格式化不必改文档。多步流程放在 `.claude/skills/`，新增或修改技能时同样走上面的更新。
