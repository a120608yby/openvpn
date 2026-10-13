# AGENTS.md

本仓库是 OpenVPN 服务端加上 Web 管理端。编码代理在改代码前先读本文件。项目事实全部写在本文件里。

## 项目概览

- 产品镜像名 `yyxx/openvpn`。容器内 OpenVPN 监听 `1194/udp`，管理页面监听 `8833`。
- Web 管理端在 `src/openvpn-web`，Go module `openvpn-web`，语言版本见 `go.mod`（当前 `go 1.25.4`）。整个目录是一个 `package main`。
- 运行时在 `build/`：`Dockerfile`、`docker-entrypoint.sh`、`supervisord.conf`、认证脚本 `openvpn-auth`。
- 裸机安装脚本是 `scripts/openvpn-install.sh`，说明在 `doc/openvpn-install.txt`。
- 许可证：MIT。

数据目录由环境变量 `OVPN_DATA` 指定，容器里是 `/data`：

| 路径 | 作用 |
| --- | --- |
| `config.json` | Viper 配置，权限 `0600`。含管理员口令、LDAP、邮件、OpenVPN 参数、`system.base.token` |
| `ovpn.db` | SQLite（`github.com/glebarez/sqlite`）。表由启动时 `AutoMigrate` 维护 |
| `pki/` | Easy-RSA 3.2.6。算法 EC `prime256v1`，默认 CA/证书/CRL 各 365 天 |
| `server.conf` | OpenVPN 配置。管理口默认 `127.0.0.1 7505` |
| `ccd/` | 每客户端配置 |
| `clients/` | 生成的客户端 `.ovpn`，经 `/ovpn/download` 提供 |
| `openvpn-status.log` | 状态日志 |

## 设置命令

本地跑管理端（PowerShell）：

```powershell
cd src/openvpn-web
go mod download
$env:OVPN_DATA = "D:\path\to\data"
New-Item -ItemType Directory -Force $env:OVPN_DATA
go run .
```

编译：

```powershell
cd src/openvpn-web
gofmt -w .
go build -o openvpn-web.exe .
```

`OVPN_DATA` 必须指向可写目录。`init()` 会 `initConfig()`，目录无效时进程会 panic。首次启动写入默认 `config.json`，默认管理员 `admin`，口令为 `admin` 的 bcrypt 哈希。

容器：

```bash
docker compose up -d
```

`build/Dockerfile` 的构建上下文是 `build/`。它从 GitHub Releases 下载已发布的 `openvpn-web`，不会编译当前 Go 源码。验证本地 Go 改动用 `go build` / `go run`，不要只用这条镜像构建判断源码是否通过。

发布：推送 git tag。`.github/workflows/build.yml` 在 `src/openvpn-web` 跑 GoReleaser，再构建 `linux/amd64`、`linux/arm64`、`linux/arm/v6`、`linux/arm/v7` 并推送镜像。

## 代码地图

路由都在 `src/openvpn-web/main.go` 的 `main()`。`AuthMiddleWare()` 从它注册之后的路由生效。`/login`、`/logout`、`/captcha` 在中间件之前。

| 文件 | 职责 |
| --- | --- |
| `main.go` | 进程入口、管理口客户端、证书解析、会话、页面和 `/ovpn` 路由 |
| `config.go` | 配置结构、默认值、监听 `config.json`。`auto_update_ovpn_config` 为真时才回写 `server.conf` |
| `user.go` | VPN 账号。密码 AES 加密；保存前 `bluemonday` 清洗，密码字段跳过清洗 |
| `group.go` | 分组与分组路由。根组名必须是 `Default` |
| `history.go` | 连接历史。每天清理，保留天数默认 90 |
| `firewall.go` | 防火墙模型，以及 nftables 表（默认名 `openvpn-nft`） |
| `access.go` | 分组访问权限。按用户名写入 nftables `access` 链 |
| `ldap.go` | LDAP 认证。开启后本地 VPN 账号不再用于登录 |
| `azure.go` | Entra ID 用户查找、组过滤和口令校验 |
| `mfa.go` | TOTP |
| `email.go` | SMTP |
| `captcha.go` | 登录失败计数与点选验证码 |
| `ovpnconfig.go` | 按行读写 `server.conf` |
| `templates/` | `//go:embed`。页面 `index.html`、`login.html`、`client.html`；脚本在 `templates/static/js/` |
| `build/openvpn-auth` | OpenVPN `auth-user-pass-verify`。用 `O-Token` 调本机 `POST /ovpn/login` |
| `build/docker-entrypoint.sh` | 初始化 PKI 与 `server.conf`，并处理 client-connect / client-disconnect |

路由分组：

- `/admin`、`/settings`、`/ovpn/*`：管理员。
- `/`、`/client/*`：已登录的 VPN 用户。非管理员访问管理路径会被重定向到 `/`。
- `POST /ovpn/login`、`/ovpn/history`、`/ovpn/firewall`：请求头 `O-Token` 等于 `system.base.token`，且来源是回环地址，才跳过会话。

前端由 `templates/static/js/index.js` 动态 `import` `utils.js`、`settings.js`、`user.js`、`client.js`、`cert.js`、`history.js`、`firewall.js`。界面文案在 `templates/static/zh.json`。

## 代码风格

- Go 用 `gofmt`。新逻辑放进对应领域文件，用结构体方法（`Create`、`Update`、`Delete`、`All`、`Get`）。只有新路由留在 `main.go`。
- 错误返回现有风格：`c.JSON(..., gin.H{"message": ...})`。用户可见文案用中文。
- 提交说明沿用仓库历史和 `.goreleaser.yml`：`feat:`、`fix:`、`refactor:`、`chore:`、`docs:`，后面用中文说明原因。
- 前端遵循 `.prettierrc.yml`：2 空格、单引号、分号、`printWidth` 120。改 `templates/` 后必须重新编译，嵌入发生在编译期。
- 不要为了整齐去重命名现有标识符：`SysBeseConfig`、`upadteOvpnConfig`、`setOnlineClinetNft`。它们已被调用点固定。

## 必须保持的行为

- 默认组网不把客户端全部流量送进 VPN。只有 `openvpn.ovpn_gateway` 为真才推送 `redirect-gateway`。
- 页面上改 OpenVPN 参数只改配置。写进 `server.conf` 要先打开「自动更新配置」；服务要另外重启才生效。关掉自动更新是为了保留手工改过的 `server.conf`。
- IPv6 要同时满足：系统设置打开 IPv6、客户端与服务端 `proto` 都是 `udp6` 或 `tcp6`、Docker 网络启用 IPv6。
- 证书有效期用环境变量覆盖：`EASYRSA_CA_EXPIRE`、`EASYRSA_CERT_EXPIRE`、`EASYRSA_CRL_DAYS`。
- 打开 `system.azure.azure_auth` 后，普通用户的 Web 登录和 `/ovpn/login` 走 `azure.go`，不再使用本地 VPN 账号。管理员登录仍在 `POST /login` 里做 bcrypt。
- Azure 与 LDAP 互斥。客户端机密以 AES 存在 `system.azure.client_secret`。`GET /settings` 返回解密后的明文。
- Entra ID 要求 MFA 或交互式条件访问时，用户名密码登录失败。口令错误是「密码错误」；MFA 是「该账号启用了 MFA，无法用用户名密码登录」；条件访问是「该账号需要交互式登录（条件访问），无法用用户名密码登录」。
- 管理员口令用 bcrypt。VPN 账号口令用 `secretKey` 做 AES。邮件口令同样 AES。不要改成明文存储。
- `gin.Recovery()` 目前被注释掉。不要无说明地打开或删掉这行。
- `build/docker-entrypoint.sh` 读取 `.openvpn.ovpn_maxclients`，Go 配置字段是 `ovpn_max_clients`。改最大客户端数时两边一起核对。
- 分组访问权限存在 `group.access_clients`、`access_subnets`、`access_rules`。空值表示继承上级，根上仍为空则客户端互访和网段都放行。例外行以 `+` 放行、以 `-` 拒绝，本节点规则先于上级匹配。
- OpenVPN 2.6 没有内置包过滤，不要加回 `minimal_pf.so` 或 `pf_file`。访问权限在客户端 `learn-address` 时经 `/ovpn/firewall?a=add_ovips` 写入 nftables 表 `access` 链；断开时删除。放行用 `return`，这样已有防火墙规则仍能拒绝。
- 启动 OpenVPN 时注释 `server.conf` 的 `client-to-client`，并打开 IP 转发。VPN 网段内部转发不做 MASQUERADE。否则客户端互访不经过防火墙，访问权限管不到。

## 测试

仓库没有测试文件，也没有 `go test` 套件。不要声称跑过不存在的测试。

改 Go 后在 `src/openvpn-web` 执行：

```powershell
gofmt -l .
go build ./...
```

`gofmt -l` 有输出就先格式化。涉及登录、证书、CCD、防火墙、LDAP、MFA 或 `server.conf` 的改动，在交付说明里写明手动验证步骤。页面改动要重新编译后再看嵌入后的页面。

## 安全

- 不要提交 `config.json`、`ovpn.db`、`pki/`、私钥、`tc.key`、token、管理员 / LDAP / 邮箱口令。这些已在 `.gitignore` 的范围内或属于运行时数据。
- 不要把管理口 `7505` 或 `/ovpn/login` 暴露到非回环地址。
- 不要为了调试关掉会话校验、证书校验、`O-Token` 校验或回环判断。
- 写入数据库的用户字符串继续走 `bluemonday`。不要把 `Password` 放进通用字符串清洗。
- `build/openvpn-auth` 和入口脚本会读 `config.json`。改 token 字段名时两处一起改。

## 文档维护

每次 bug 修复、技能更新或新增（功能，或仓库内代理技能）后，同一次变更里改完这三处，每处自己写全：

- 本文件：受影响的命令、目录、约定、已知限制
- `README.md`：使用者能看到的功能、部署方式和注意事项
- 仓库根目录里另一份编码代理规范：同一批事实，按那份文件自己的章节写入

纯格式化、不改变行为、命令、路径或限制时，不必改这些文档。本仓库目前没有项目级代理技能；一旦新增或修改技能，把触发条件、边界和验证方式写进本文件对应小节。
