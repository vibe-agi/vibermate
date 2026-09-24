# 部署与 HTTPS：先选场景

先回答“谁要从哪里连接”，不用先理解 CA、SAN 或容器网络。App、原生 Web 和容器 Web
使用同一个 Runtime、账号体系和证书边界。

| 场景 | 推荐入口 | 地址与证书 |
| --- | --- | --- |
| 个人 App | 打开 App，安装终端命令，运行 `vibermate run -- codex` | 不需要网页账号、域名或全局安装 CA |
| 同一台电脑的原生 Web | `vibermated server` | `http://127.0.0.1:9666`，不需要证书 |
| 同一台电脑的容器 Web | `compose.yaml` | 固定发布到宿主机回环 HTTP |
| 私网/VPN，没有公网域名 | `private_ca_tls` 或 `compose.private.yaml` | ViberMate 私有 CA；可用 hosts 名称或 IP 证书 |
| 有公网域名，希望自动维护 | `automatic_tls` 或 `compose.public.yaml` | 自动申请、续期并热加载公共证书 |
| 已有公共/企业证书 | `tls_files` 或 `compose.team.yaml` | 部署者提供完整证书链和私钥 |

“个人使用”不代表远程 HTTP 安全。只要浏览器或 CLI 跨设备连接，就选择一种 HTTPS
模式，或使用经过身份验证的可信隧道。不要把 `0.0.0.0`、容器的 `172.x` 地址或证书
警告页发给用户。

## 三条连接，三种信任

1. 浏览器/CLI → ViberMate：由本页配置的 **服务器 HTTPS 证书**保护。
2. Agent → 被检查的 AI 域名：由 **AI 流量检查 CA（Proxy CA）**签发目标域名叶证书。
3. Runtime → 真正的 AI 服务商：严格验证服务商自己的证书，并使用选定网络出口。

公共自动证书和部署者证书与 Proxy CA 完全不同。`private_ca_tls` 是一个明确的例外：
为了让无公网域名的受管设备少维护一套根，它目前使用同一个 ViberMate 私有 CA 签发
Runtime 服务器叶证书。因此把该 CA 加入系统信任会同时信任这台 Runtime 的流量检查
能力，只应安装到受管设备，不应公开分发。

## 本机原生 Web

从发布包目录运行：

```sh
./vibermated server
```

打开 <http://127.0.0.1:9666>。默认仅监听回环地址。端口冲突时使用
`--listen 127.0.0.1:9667`，浏览器与 CLI 一起改端口。另开一个终端读取初始化/恢复密钥：

```sh
./vibermated server recovery-key
```

在网页创建所有者账号。没有默认 `admin/admin`。若启动时指定了 `--data-dir`，所有
服务器本地命令都要使用同一个绝对目录。CLI 显式连接独立 Server：

```sh
vibermate login --server http://127.0.0.1:9666
vibermate run --server http://127.0.0.1:9666 -- codex
```

不带 `--server` 的本地 `vibermate run` 连接 App。System Transparent 默认不保留
对话正文；需要内容时发布流量策略，并用 `--env <策略ID>` 选择。

### Web 中为其他客户端创建专用代理登录

所有者登录 Web 工作台，在「流量 → 运行记录」点创建专用代理登录，选择流量策略、客户端
类型和有效期，核对地址与受检 AI 域名后创建。代理用户名、密码只在创建或轮换后显示一次；
轮换会立即使旧密码失效，撤销会停止新流量，但不会删除已有记录。普通团队成员不能创建
这类登录。

同机原生 Web 显示本机回环代理地址；远程 Web 使用配置的客户端可达 `--access-address`
及其 HTTPS 证书身份，不会把 `0.0.0.0`、容器内网地址或服务器上的证书文件路径交给客户端。
从交付页下载 **Proxy CA**，核对显示的 SHA-256 指纹，再按客户端要求安装到实际发起
AI 请求的设备。它用于校验被检查的 AI 域名；外层代理连接仍需独立验证 **服务器 HTTPS
证书**。使用 `private_ca_tls` 时，当前安装的私有根可能同时签发服务器叶证书；使用
公共/企业证书时，下载 Proxy CA 不会修复服务器证书错误。远程客户端优先使用 HTTPS，
HTTP 只适合受信任的本机或私网环境。

## 私网 HTTPS：没有公网域名

### 方案 A：自定义名称 + hosts 文件

选择稳定、仅内部使用的名称，例如 `vibermate.home.arpa`：

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --access-address vibermate.home.arpa:9666 \
  --transport private_ca_tls
```

在每台客户端的 hosts 文件加入实际服务器地址，例如：

```text
192.168.1.20  vibermate.home.arpa
```

Unix/macOS 文件为 `/etc/hosts`；Windows 为
`C:\Windows\System32\drivers\etc\hosts`。浏览器与 CLI 都使用
`https://vibermate.home.arpa:9666`，不要改回 IP，否则名称校验会失败。

### 方案 B：直接使用 IP

IP 稳定时无需 hosts 文件：

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --access-address 192.168.1.20:9666 \
  --transport private_ca_tls
```

证书会包含 IP SAN，客户端使用 `https://192.168.1.20:9666`。不要把 DHCP 地址当成
稳定身份；地址变化后更新 `--access-address` 并重启，ViberMate 会用同一 CA 重签
叶证书，已正确信任 CA 的客户端不需要重新安装根。

### 安全地取得并信任 CA

先启动一次 Server，再在服务器本机执行：

```sh
./vibermated server ca-certificate > vibermate-private-ca.crt
openssl x509 -in vibermate-private-ca.crt -noout -fingerprint -sha256
```

将指纹与启动日志中的 `caFingerprint` 通过独立可信渠道核对，再把公开证书导入每台
受管客户端的系统/浏览器信任库。该命令只读取公开证书，不打开或导出 CA 私钥。
不要先忽略浏览器警告，再从同一个未信任页面下载 CA；那不能建立安全的首次信任。

CLI 会优先使用系统根验证。旧的精确叶指纹记录可在 CA 已安装后显式迁移：

```sh
vibermate trust --server https://vibermate.home.arpa:9666 --system-roots
```

## 自动公共 HTTPS

初始实现支持一个可公开签发的 DNS 名称、HTTP-01 或 TLS-ALPN-01，不支持私网名称、
IP、通配符或 DNS-01。ViberMate 使用 CertMagic 管理证书状态，证书持久化在数据目录的
`server-https` 中，续期成功后热加载，不需要替换 Proxy CA。

最简单的原生部署让公网 TCP 443 转发到进程监听端口（示例为 8443）：

```sh
./vibermated server \
  --listen 0.0.0.0:8443 \
  --access-address runtime.example.com:443 \
  --transport automatic_tls \
  --acme-agree-terms \
  --acme-email admin@example.com \
  --acme-challenge tls_alpn_01
```

公网 DNS 必须先解析到该服务器，公网 443 必须原样到达监听端口。若直接监听 443，
请用服务管理器授予最小的低端口绑定能力，不要以 root 运行整个 Runtime。

HTTP-01 可用于公网 80 转发到一个非特权内部端口：增加
`--acme-challenge http_01 --acme-http-port 8080`，并让公网 80 转发到本机 8080。
`--access-address` 仍是用户实际打开的 HTTPS 地址。启动命令明确同意签发机构条款，并会
把域名和可选联系邮箱发送给所选 CA；自定义 ACME 目录可用专家参数 `--acme-ca`。

证书正在申请、续期或失败时，「设置 → 安全与数据 → 连接到服务器」会显示真实状态和
错误。TLS-ALPN 首次申请是异步的，进程已启动不等于证书已经可用。

## 使用已有证书

证书必须覆盖 `--access-address` 中的准确域名/IP；不匹配时 Server 拒绝启动：

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --access-address runtime.example.com:9666 \
  --transport tls_files \
  --tls-cert /absolute/path/fullchain.pem \
  --tls-key /absolute/path/privkey.pem
```

证书和密钥必须是普通文件，私钥仅运行用户可读（`0600`）。ViberMate 不复制或改写
这些文件；当前在替换后需要重启服务。使用 Caddy/Nginx 等外部入口时，普通 HTTP
反向代理不一定支持同端口的认证 CONNECT；需验证四层透传，不能只验证网页能打开。

## 账号与页面

存储位置在「设置 → 安全与数据」中查看。App 可以选择本机新目录并安全迁移；原生 Web
与容器 Web 使用服务器的 `--data-dir` / 持久化卷，不使用浏览器电脑的目录。步骤、备份
边界与读取性能说明见 [数据位置与读取性能](storage-and-read-performance.md)。

- 「接入与启动」只回答如何登录和启动；不会混入用户表或证书私钥。
- 「用户管理」创建、重置、停用 Runtime 用户；上游服务账号在另一套配置中。
- 「安全与数据」分别显示服务器连接证书、Proxy CA、数据留存。阻断错误不折叠，
  签发者、指纹、验证方式等专家信息放在详情中。

每个人使用自己的账号登录网页和 CLI。密码可自行修改，所有者可重置成员密码。恢复
密钥只在服务器本机读取，使用后轮换，不能分享给成员。

## 诊断与验证

查看所有服务器参数和三条最短示例：

```sh
./vibermated server --help
```

本地冒烟测试使用临时数据，不读取现有用户数据：

```sh
node tool/server/smoke-native.mjs
node tool/docker/smoke-local.mjs
```

真实公共 ACME 仍需在拥有可控 DNS/端口的预发布环境验收；单元测试不会向公共 CA
申请证书。容器具体命令、数据卷与回滚见 [Docker 部署](docker.md)。
