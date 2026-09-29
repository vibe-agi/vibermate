[English](deployment.md) · [简体中文](deployment.zh-CN.md)

# 部署与 HTTPS：先选场景

先回答“谁要从哪里连接”，不用先理解 CA、SAN 或容器网络。App、原生 Web 和容器 Web
使用同一个 Runtime、同一套账号体系和同样的证书边界。

| 场景 | 推荐入口 | 地址与证书 |
| --- | --- | --- |
| 个人 App | 打开 App，安装终端命令，运行 `vibermate run -- codex` | 不需要网页账号、域名或全局安装 CA |
| 同一台电脑的原生 Web | `vibermated server` | `http://127.0.0.1:9666`，不需要证书 |
| 同一台电脑的容器 Web | `compose.yaml` | 固定发布到宿主机回环地址，使用 HTTP |
| 私网/VPN，没有公网域名 | `private_ca_tls` 或 `compose.private.yaml` | ViberMate 私有 CA；可用 hosts 名称或 IP 证书 |
| 有公网域名，希望自动维护证书 | `automatic_tls` 或 `compose.public.yaml` | 自动申请、续期并热加载公共证书 |
| 已有公共/企业证书 | `tls_files` 或 `compose.team.yaml` | 部署者提供完整证书链和私钥 |

“个人使用”不代表远程 HTTP 就安全。只要浏览器或 CLI 跨设备连接，就选择一种 HTTPS
模式，或使用经过身份验证的可信隧道。不要把 `0.0.0.0`、容器的 `172.x` 地址或证书
警告页发给用户。

## 三条连接，三种信任

1. 浏览器/CLI → ViberMate：由本页配置的 **服务器 HTTPS 证书**保护。
2. Agent → 被检查的 AI 域名：由 **AI 流量检查 CA（Proxy CA）**为目标域名签发叶证书。
3. Runtime → 真正的 AI 服务商：严格验证服务商自己的证书，并使用选定的网络出口。

公共自动证书和部署者提供的证书与 Proxy CA 完全无关。`private_ca_tls` 是一个明确的
例外：为了让没有公网域名的受管设备少维护一套根证书，它使用同一个 ViberMate 私有 CA
签发 Runtime 的服务器叶证书。因此，把这个 CA 加入系统信任，也就同时信任了这台
Runtime 的流量检查能力。只把它安装到受管设备，不要公开分发。

## 本机原生 Web

在发布包目录中运行：

```sh
./vibermated server
```

打开 <http://127.0.0.1:9666>。默认只监听回环地址。端口冲突时使用
`--listen 127.0.0.1:9667`，浏览器和 CLI 也要一起改用新端口。另开一个终端，读取
初始化/恢复密钥：

```sh
./vibermated server recovery-key
```

在网页中创建所有者账号。没有默认的 `admin/admin`。如果启动时指定了 `--data-dir`，
所有服务器本地命令都要使用同一个绝对路径。CLI 需要显式连接独立 Server：

```sh
vibermate login --server http://127.0.0.1:9666
vibermate run --server http://127.0.0.1:9666 -- codex
```

不带 `--server` 的本地 `vibermate run` 连接的是 App。默认流量策略 System
Transparent 会记录对话（默认保留 30 天），并保持原始目标地址和凭据不变。需要不同的
路由、记录或保留方式时，发布你自己的流量策略，再用 `--env <策略ID>` 选择它。

### 在 Web 中为其他客户端创建专属代理登录

所有者登录 Web 工作台，在「流量 → 运行记录」中点「创建专属代理登录」，选择流量策略、
客户端类型和有效期，核对地址与受检 AI 域名后创建。代理用户名和密码只在创建或轮换后
显示一次。轮换会让旧密码立即失效；撤销会停止新流量，但不会删除已有记录。普通团队
成员不能创建这类登录。

同机原生 Web 显示本机回环代理地址。远程 Web 使用配置的、客户端可以访问的
`--access-address` 及其 HTTPS 证书身份，不会把 `0.0.0.0`、容器内网地址或服务器上的
证书文件路径交给客户端。

从交付页下载 **Proxy CA**，核对页面显示的 SHA-256 指纹，再按客户端的要求安装到实际
发起 AI 请求的设备上。它只用于校验被检查的 AI 域名；外层代理连接仍需单独验证
**服务器 HTTPS 证书**。使用 `private_ca_tls` 时，同一个私有根也签发服务器叶证书；
使用公共/企业证书时，下载 Proxy CA 不能修复服务器证书错误。远程客户端优先使用
HTTPS，HTTP 只适合受信任的本机或私网环境。

## 私网 HTTPS：没有公网域名

### 方案 A：自定义名称 + hosts 文件

选择一个稳定、仅内部使用的名称，例如 `vibermate.home.arpa`：

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --access-address vibermate.home.arpa:9666 \
  --transport private_ca_tls
```

在每台客户端的 hosts 文件中加入服务器的实际地址，例如：

```text
192.168.1.20  vibermate.home.arpa
```

Unix/macOS 的文件是 `/etc/hosts`；Windows 是
`C:\Windows\System32\drivers\etc\hosts`。浏览器和 CLI 都使用
`https://vibermate.home.arpa:9666`。不要改回 IP，否则名称校验会失败。

### 方案 B：直接使用 IP

IP 稳定时不需要 hosts 文件：

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --access-address 192.168.1.20:9666 \
  --transport private_ca_tls
```

证书会包含 IP SAN，客户端使用 `https://192.168.1.20:9666`。不要把 DHCP 分配的地址
当作稳定身份。地址变化后，更新 `--access-address` 并重启；ViberMate 会用同一个 CA
重新签发叶证书，已经正确信任该 CA 的客户端不需要重新安装根证书。

### 安全地取得并信任 CA

先启动一次 Server，然后在服务器本机执行：

```sh
./vibermated server ca-certificate > vibermate-private-ca.crt
openssl x509 -in vibermate-private-ca.crt -noout -fingerprint -sha256
```

该命令直接从服务器数据目录读取公开的 CA 证书，不会打开或导出 CA 私钥。因为它在
服务器本机运行，这里算出的指纹就是可信的参考值。通过独立的可信渠道（例如当面或
已验证的聊天）把证书文件和这个指纹交给每台受管客户端；客户端在导入系统/浏览器信任库
之前，先对收到的文件运行同样的 `openssl` 命令，确认指纹一致。不要先忽略浏览器警告，
再从同一个未受信任的页面下载 CA；那样无法建立安全的首次信任。

CLI 优先使用系统根证书验证。首次连接到私有 CA 签发的 Server 时，CLI 会把该 CA
限定到准确的 Server 主机和端口，之后正常换发叶证书不需要重新信任。这不会把 CA
安装到系统，也不能代替 Agent 的 Proxy CA 配置。首次连接仍然是 TOFU（首次使用即
信任），因此建议在登录、发送密码之前，先用通过独立可信渠道核对过的 CA SHA-256 指纹
建立信任（64 位十六进制，不含冒号）：

```sh
vibermate trust --server https://vibermate.home.arpa:9666 --ca-fingerprint <已核对的CA指纹>
```

该命令只做 TLS 握手，不发送登录信息，并验证主机名、有效期和证书链。它也可以把该
Server 已保存的叶证书指纹显式改为 CA 信任；失败时不会改变原有信任。已保存的叶证书
指纹不会在连接时自动升级为 CA 信任。如果已经把 CA 安装到本机系统信任库，可以改用
系统根验证：

```sh
vibermate trust --server https://vibermate.home.arpa:9666 --system-roots
```

## 自动公共 HTTPS

当前支持一个可公开签发的 DNS 名称，使用 HTTP-01 或 TLS-ALPN-01 验证；不支持私网
名称、IP、通配符或 DNS-01。ViberMate 使用 CertMagic 管理证书状态，证书保存在数据
目录的 `server-https` 中，续期成功后热加载，不需要替换 Proxy CA。

最简单的原生部署是让公网 TCP 443 转发到进程的监听端口（示例为 8443）：

```sh
./vibermated server \
  --listen 0.0.0.0:8443 \
  --access-address runtime.example.com:443 \
  --transport automatic_tls \
  --acme-agree-terms \
  --acme-email admin@example.com \
  --acme-challenge tls_alpn_01
```

公网 DNS 必须先解析到这台服务器，公网 443 必须原样到达监听端口。如果直接监听 443，
请用服务管理器只授予绑定低端口所需的最小能力，不要以 root 身份运行整个 Runtime。

HTTP-01 适合把公网 80 转发到一个非特权内部端口：加上
`--acme-challenge http_01 --acme-http-port 8080`，并让公网 80 转发到本机 8080。
不写 `--acme-challenge` 时默认使用 HTTP-01；此时不写 `--acme-http-port` 则使用 80
端口。`--access-address` 始终是用户实际打开的 HTTPS 地址。`--acme-agree-terms`
表示你明确同意签发机构的条款；启动后会把域名和可选的联系邮箱发送给所选 CA。需要
自定义 ACME 目录时，可以使用专家参数 `--acme-ca`。

证书正在申请、续期或申请失败时，「设置 → 安全与数据 → 连接到服务器」会显示真实状态和
错误。TLS-ALPN 的首次申请是异步的：进程已启动，不等于证书已经可用。

## 使用已有证书

证书必须覆盖 `--access-address` 中的准确域名或 IP；不匹配时 Server 会拒绝启动：

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --access-address runtime.example.com:9666 \
  --transport tls_files \
  --tls-cert /absolute/path/fullchain.pem \
  --tls-key /absolute/path/privkey.pem
```

证书和私钥必须是普通文件（不能是符号链接），私钥只允许运行用户读取（`0600`）。
ViberMate 不会复制或改写这些文件；替换证书后需要重启服务。使用 Caddy、Nginx 等外部
入口时，普通 HTTP 反向代理不一定支持在同一端口上进行带认证的 CONNECT。请验证四层
透传是否可用，不能只验证网页能否打开。

## 账号与页面

存储位置在「设置 → 安全与数据」中查看。App 可以选择本机的新目录，并安全地把数据移动
过去；原生 Web 和容器 Web 使用服务器的 `--data-dir` 或持久化卷，而不是浏览器所在电脑的
目录。具体步骤、备份范围与读取性能见
[数据位置与读取性能](storage-and-read-performance.md) 和
[备份与恢复](backup-and-restore.zh-CN.md)。

- 「接入与启动」只说明如何登录和启动，不会混入用户列表或证书私钥。
- 「用户管理」用于创建、重置、停用 Runtime 用户；上游服务账号在另一处配置。
- 「安全与数据」分别显示服务器连接证书、Proxy CA 和数据留存。阻断性错误不会折叠，
  签发者、指纹、验证方式等专家信息放在详情中。

每个人都用自己的账号登录网页和 CLI。密码可以自行修改，所有者可以重置成员密码。恢复
密钥只能在服务器本机读取，使用后应轮换，不能分享给成员。

## 诊断与验证

查看常用的三种部署示例、服务器本地命令和离线数据命令：

```sh
./vibermated server --help
```

本地冒烟测试使用临时数据，不读取现有用户数据：

```sh
node tool/server/smoke-native.mjs
node tool/docker/smoke-local.mjs
```

真实的公共 ACME 签发仍需在拥有可控 DNS 和端口的预发布环境中验收；单元测试不会向
公共 CA 申请证书。容器的具体命令、数据卷与回滚见 [Docker 部署](docker.zh-CN.md)。
