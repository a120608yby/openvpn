# openvpn

![image-20240529110841439](https://raw.githubusercontent.com/GavinTan/files/master/picgo/image-20240529110841439.png)

![20220930173030](https://raw.githubusercontent.com/GavinTan/files/master/picgo/20220930173030.png)

![20220930173103](https://raw.githubusercontent.com/GavinTan/files/master/picgo/20220930173103.png)

![image-20241015170847764](https://raw.githubusercontent.com/GavinTan/files/master/picgo/image-20241015170847764.png)

**openvpn 安全与加密相关配置参考于[openvpn-install](https://github.com/angristan/openvpn-install?tab=readme-ov-file#security-and-encryption)的Security and Encryption部分。**

> 提示：
>
> 1. 管理员登录账号密码默认`admin:admin`，登录后可在系统设置里修改。
> 2. 系统同时支持使用vpn账号登录，vpn账号登录后可设置mfa、修改密码、下载配置文件。
> 3. 第一步：web -> 管理 -> 客户端里生成下载客户端配置文件。
> 4. 第二部：web -> 管理 -> VPN 账号里管理添加账号（默认启用账号验证可在 VPN 账号里开启或关闭）。
> 5. 第三步：使用vpn账号登录 -> web -> 下载客户端 （设置mfa、修改密码）-> 下载配置文件 -> 导入客户端进行连接。
>
> 
>
> 注意：
>
> 1. 默认场景应用于组网禁用vpn网关，如果需要客户端所有流量都走 openvpn 需要在系统设置openvpn里启用vpn网关。
> 2. 系统设置里修改openvpn配置生效必须启用自动更新配置，如果要保留自己修改的server.conf配置则需要禁用自动更新配置。
> 3. 系统设置openvpn配置只是修改了server.conf，应用需要手动重启openvpn服务。
> 4. 设置mfa需要使用要设置的vpn账号登录用户页面后进行mfa设置，在用户页面下载配置文件会动态更新配置文件mfa参数，客户端页面的开启mfa选项是直接在配置文件添加mfa参数（直接在后台下载配置文件需要设置该选项，通过用户页面下载配置文件可忽略）。
> 5. 在创建客户端后关闭账号验证客户端的配置文件存在auth-user-pass参数客户端会依旧弹出登录，登录信息可以随便输入不会做验证，若有弹窗困扰的建议手动编辑客户端配置文件注释掉参数或重新生成客户端配置文件。
> 5. 默认生成的证书一年有效期，如果想要设置更长时间的有效期可以添加环境变量EASYRSA_CA_EXPIRE、EASYRSA_CERT_EXPIRE、EASYRSA_CRL_DAYS设置来覆盖默认设置，例：EASYRSA_CA_EXPIRE=3650。

## 支持功能

- 账号管理
- 证书管理
- ipv6支持
- ldap支持
- mfa支持
- 邮件通知
- 连接历史记录
- vpn账号固定ip
- vpn账号分组路由推送
- 按分组控制客户端能访问的地址
- 在线编辑server.conf
- 在线重启openvpn服务
- 一键生成客户端 & CCD配置文件

## Quick Start

> 裸机部署：目前提供一个支持部分发行版的安装脚本`scripts/openvpn-install.sh`。

运行 openvpn

```shell
docker run -d \
  --cap-add=NET_ADMIN \
  -p 1194:1194/udp \
  -p 8833:8833 \
  -v $(pwd)/data:/data \
  yyxx/openvpn
```

### compose

- 安装 docker-compose

  ```bash
  yum install -y docker-compose-plugin
  ```
  
- 创建 docker-compose.yml

  ```yaml
  services:
    openvpn:
      image: yyxx/openvpn
      cap_add:
        - NET_ADMIN
      ports:
        - "1194:1194/udp"
        - "8833:8833"
      volumes:
        - ./data:/data
        - /etc/localtime:/etc/localtime:ro
  ```
  
- 运行 openvpn

  ```bash
  docker compose up -d
  ```



## IPV6

>注意：
>
>1. 需要在页面系统设置openvpn里启用ipv6
>2. 启用ipv6后客户端跟服务器的proto需要都指定udp6/tcp6
>3. docker的网络需要启用ipv6支持
>4. 使用openvpn-connect客户端的需要使用3.4.1以后的版本

```bash
services:
  openvpn:
    image: yyxx/openvpn
    cap_add:
      - NET_ADMIN
    ports:
      - "1194:1194/udp"
      - "8833:8833"
    volumes:
      - ./data:/data
      - /etc/localtime:/etc/localtime:ro
    sysctls:
      - net.ipv6.conf.default.disable_ipv6=0
      - net.ipv6.conf.all.forwarding=1

networks:
  default:
    enable_ipv6: true
```

## 访问权限

在「VPN 账号」里对分组右键，选择「访问权限」。规则作用于该节点下的账号；某一项留空时继续用上级的设置，一直到根节点。根节点也没设置时，默认全部放行。

- **客户端互访**：能否访问其他 VPN 客户端。
- **网段默认**：其余目标地址默认放行还是拒绝。
- **网段例外**：一行一条，`+10.32.161.0/24` 表示放行，`-10.32.165.0/24` 表示拒绝。先写到的先生效，本节点的例外写在上级例外之前。

这套规则对应常见的按用户名放行网段的做法。当前 OpenVPN 2.6 已经去掉内置包过滤，所以规则写进 nftables 的 `access` 链，在客户端拿到地址时生效，断开时删掉。放行不会绕过已有的防火墙策略，拒绝会直接丢弃。

保存后会立刻刷新已在线、且属于该节点的客户端。刷新失败时，重连后生效。拒绝客户端互访时，VPN 服务器自己的地址仍然放行。

客户端之间的流量要经过本机防火墙，这条规则才管得住。OpenVPN 启动时会注释 `server.conf` 里的 `client-to-client`，打开 IP 转发，并且不再对「源和目标都在 VPN 网段」的流量做地址伪装。已经在跑的服务需要重启一次 OpenVPN。

LDAP 认证时如果本地没有同名 VPN 账号，不会套用这套规则。

## LDAP

> 在系统设置里启用LDAP认证，启用LDAP认证后本地的VPN账号将不在工作。

部分参数说明：

- LDAP_URL：ldap连接TLS 例：ldaps://example.org:636
- LDAP_USER_ATTRIBUTE：根据当前使用的LDAP服务器设置，例：OpenLDAP：uid ； Windows AD: sAMAccountName
- LDAP_USER_ATTR_IPADDR_NAME：可在ldap服务器添加ipaddr自定义字段，也可以设置为ldap已经存在其他的未使用字段 例：mobile、homePhone
- LDAP_USER_ATTR_CONFIG_NAME：可在ldap服务器添加config自定义字段，也可以设置为ldap已经存在其他的未使用字段 例：mobile、homePhone

## Azure

在系统设置里启用 Azure 认证后，本地 VPN 账号不再用于 Web 登录和客户端拨入。管理员仍使用本地账号。Azure 与 LDAP 同时只能开启一个，打开其中一个会关闭另一个。

在 Microsoft Entra ID 注册单租户应用：创建客户端机密，授予应用程序权限 `User.Read.All` 并完成管理员同意。使用组过滤时再授予 `GroupMember.Read.All`。身份验证中打开「允许公共客户端流」。不需要重定向 URI。

登录名默认匹配 `userPrincipalName`，也可改为 `mail`。固定 IP 与客户端配置文件名写在用户的扩展属性上，默认分别是 `onPremisesExtensionAttributes.extensionAttribute1` 和 `extensionAttribute2`。启用了 MFA 或条件访问的账号无法用用户名密码登录。

## 文档维护

每次 bug 修复、技能更新或功能增加，都要在同一次变更里同步更新下面三份文档。每份按自己的读者写全，规范文件之间不要互相引用：

- `README.md`：使用者能看到的功能、部署步骤和注意事项
- `AGENTS.md`：Cursor 等编码代理使用的项目说明（命令、目录、约定、限制）
- `CLAUDE.md`：Claude Code 使用的项目说明（命令、目录、约定、限制）

只改格式、不改变行为、命令、路径或限制时，不必改这三份文档。
