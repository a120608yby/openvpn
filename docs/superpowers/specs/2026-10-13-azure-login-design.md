# Azure 登录设计

日期：2026-10-13

普通用户在打开 Azure 认证后，Web 登录和 OpenVPN 拨入改为用用户名密码向 Microsoft Entra ID 校验，用户属性从 Microsoft Graph 读取。行为对齐现有 LDAP。管理员仍使用本地账号。

## 范围

包含：

- 系统设置中的 Azure 配置，以及与 LDAP 开关的互斥。
- `User.Login` 的 Azure 分支，同时覆盖 Web 普通用户登录和 `POST /ovpn/login` 拨入认证。
- 用户页读取配置名、固定 IP，并禁止在本系统改密码、设置 MFA。
- 管理端 VPN 账号页在 Azure 开启时的提示。

不包含：

- 登录页跳转到 Microsoft。登录表单保持用户名和密码。
- 替换管理员本地账号。`POST /login` 里用户名等于管理员时，仍只做本地 bcrypt 校验。
- 自动创建本地 VPN 账号，或把 Azure 用户写入 `ovpn.db`。
- 本地分组路由。Azure 用户不写 `.ovc`，与 LDAP 相同。
- OpenVPN 认证脚本 `build/openvpn-auth` 的协议。它继续把用户名和密码交给 `/ovpn/login`。

## 行为

`system.azure.azure_auth` 为真时：

- 普通用户不再查本地 `User` 表。
- 拨入认证走同一套 Azure 校验。重复登录上限仍在进入 Azure 之前判断，沿用 `User.Login` 里现有的 `max_duplicate_login` 逻辑。
- 打开 Azure 且校验通过时，把 `system.ldap.ldap_auth` 设为假。打开 LDAP 且校验通过时，把 `azure_auth` 设为假。校验失败则整次保存拒绝，另一个开关保持原值。保存成功后，页面取消另一个开关的勾选，并提示已关闭哪一种认证。

认证顺序：

1. 用租户 ID、应用程序 ID、客户端机密，向 `https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token` 申请应用令牌。`grant_type=client_credentials`，`scope=https://graph.microsoft.com/.default`。
2. 用该令牌调用 Microsoft Graph，按 `user_attribute` 精确匹配一个用户。默认属性是 `userPrincipalName`，也可配置为 `mail`。过滤值中的单引号按 OData 规则写成两个单引号。
3. `group_filter` 为真时，调用 `POST /users/{id}/checkMemberGroups`，请求体为配置的 `group_id`。返回列表不含该组时，按「用户不存在」拒绝。
4. 再用同一用户名和密码换令牌，只为验证口令。`grant_type=password`，`scope=openid`。这个令牌不用于读 Graph。
5. 拨入（`clogin` 为真）且口令正确时：若开启了「校验客户端配置」，Graph 上的配置名去掉 `.ovpn` 后缀后必须等于证书通用名，否则返回「使用非法配置文件登录」。IP 属性非空时写入 `OVPN_DATA/.ovip`。

用户页：

- 用户信息和下载配置使用 Graph 上的配置名、IP，不读本地用户表。
- 配置名为空时返回「该账号未指定配置文件，请联系管理员」。
- 改密码返回「Azure用户不支持修改密码」。
- 设置 MFA 返回「Azure用户不支持设置MFA」。

管理端打开「VPN 账号」页时，若 Azure 已启用，提示本地 VPN 账号将不再用于登录。文案位置与现有 LDAP 提示相同。

## 配置

写在 `config.json` 的 `system.azure`。客户端机密在 `POST /settings` 时用 `system.base.secret_key` 做 AES 加密，算法与邮件口令相同。`GET /settings` 返回前解密该字段，页面持有明文；再次保存时加密的是明文，避免把密文再加密一次。

| 配置键 | 默认值 | 作用 |
| --- | --- | --- |
| `azure_auth` | `false` | 是否启用 |
| `tenant_id` | 空 | 租户 ID |
| `client_id` | 空 | 应用程序 ID |
| `client_secret` | 空 | 客户端机密，落盘为 AES 密文 |
| `user_attribute` | `userPrincipalName` | 登录名匹配字段，允许 `userPrincipalName` 或 `mail` |
| `group_filter` | `false` | 是否要求属于指定安全组 |
| `group_id` | 空 | 安全组对象 ID |
| `attr_ipaddr` | `onPremisesExtensionAttributes.extensionAttribute1` | 固定 IP |
| `attr_config` | `onPremisesExtensionAttributes.extensionAttribute2` | 客户端配置文件名 |

启用 Azure 时，租户 ID、应用程序 ID、客户端机密都必须已填写，否则拒绝这次保存并提示先填写这三项，且不关闭 LDAP。启用组过滤时 `group_id` 必须已填写，否则同样拒绝保存。`user_attribute` 只接受上表两个值。

`attr_ipaddr` 与 `attr_config` 是 Graph 用户对象上的点分路径。查询时 `$select` 取第一个点之前的属性，再按路径读取。管理员把固定 IP 和 `.ovpn` 文件名写在 Entra ID 用户的对应扩展属性上。

## Azure 应用

应用注册为单租户，并完成：

- 创建客户端机密。
- 应用程序权限 `User.Read.All`，并授予管理员同意。
- 使用组过滤时再加应用程序权限 `GroupMember.Read.All`，并授予管理员同意。
- 身份验证中打开「允许公共客户端流」。

不配置重定向 URI。

## 代码

| 位置 | 改动 |
| --- | --- |
| `src/openvpn-web/config.go` | 增加 `SysAzureConfig`、默认值和加载 |
| `src/openvpn-web/azure.go` | 新建。负责应用令牌、查找用户、校验组成员、校验口令、按路径读属性。对外提供与 LDAP 同职责的认证和读取 |
| `src/openvpn-web/user.go` | `Login` 在 Azure 开启时走 Azure 分支，否则保持现有 LDAP 与本地分支 |
| `src/openvpn-web/main.go` | 用户信息、下载配置、改密码、MFA 增加 Azure 分支。`POST /settings` 加密客户端机密，并处理两个开关互斥 |
| `templates/index.html`、`templates/static/js/settings.js` | LDAP 配置下方增加 Azure 配置，交互方式与 LDAP 各项相同 |
| `templates/static/js/index.js` | VPN 账号页增加 Azure 已启用提示 |
| `templates/static/js/firewall.js` | Azure 开启时禁用防火墙里的用户组选择，与 LDAP 开启时相同。Azure 用户不在本地分组中 |
| `templates/client.html` | Azure 用户禁止改密码 |

每次外呼超时 10 秒。日志不记录口令、客户端机密和应用令牌。

## 错误

用户可见文案使用中文，HTTP 状态与现有登录失败一致。

| 情况 | 文案 |
| --- | --- |
| 没有匹配用户，或组过滤未通过 | 用户不存在 |
| 匹配到多个用户 | 用户存在多个条目 |
| 口令错误（如 `AADSTS50126`） | 密码错误 |
| 需要 MFA（如 `AADSTS50076`、`AADSTS50079`） | 该账号启用了 MFA，无法用用户名密码登录 |
| 条件访问要求交互（如 `AADSTS50058`、`AADSTS50158`、`AADSTS53003`） | 该账号需要交互式登录（条件访问），无法用用户名密码登录 |
| 未开启公共客户端流（`AADSTS7000218`） | Azure 连接失败：应用未开启允许公共客户端流 |
| 租户、应用或机密错误，令牌或 Graph 调用失败，或组接口权限不足 | Azure 连接失败：加上接口返回的简短原因 |
| 拨入配置名与证书通用名不一致 | 使用非法配置文件登录 |
| 用户页改密码 | Azure用户不支持修改密码 |
| 用户页设置 MFA | Azure用户不支持设置MFA |
| 配置属性为空 | 该账号未指定配置文件，请联系管理员 |

组过滤未通过与用户不存在使用同一文案，避免暴露用户是否在组内。组接口因权限失败时走「Azure 连接失败」，不伪装成用户不存在。

## 验收

仓库没有测试套件。实现时在 `src/openvpn-web` 执行：

```powershell
gofmt -l .
go build ./...
```

`gofmt -l` 有输出则先格式化。手动确认：

- 管理员本地账号可以进入 `/admin`，口令不会发往 Azure。
- Azure 用户可以进入用户页，并按 Graph 中的配置名下载客户端配置。
- 错误口令被拒绝。
- 打开 Azure 后，本地 VPN 账号不能登录，LDAP 开关自动关闭。
- OpenVPN 拨入使用同一套用户名密码校验；配置了 IP 属性时写入 `.ovip`。
- Azure 用户不能在本系统修改密码或设置 MFA。

同一次实现变更更新 `README.md`、`AGENTS.md`、`CLAUDE.md`：说明 Azure 认证的开启条件、与 LDAP 互斥、MFA 账号无法使用用户名密码登录，以及扩展属性的用途。
