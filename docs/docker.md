# Docker 部署

仓库根目录的 `Dockerfile` 将官方 v0.1.10 Linux 发布包封装为本地镜像，包含
`vibermated`、`vibermate` 和 Flutter Web。发布包源码提交为
`1adc765bd34331e2115339425e45e4b55190f26e`，支持 ARM64 和 x86-64，并按架构
检查固定的 SHA-256 和二进制中记录的源码提交。`runtime` 构建目标不包含工作区中的源码修改，
也不是上游官方容器镜像。Compose 当前选择 `local` 构建目标：用下述构建脚本将工作区
中的 Server、CLI 和 Web 编译并覆盖到发布包之上，产出 `vibermate-runtime:0.1.10-local`。
仅更新仓库或执行 `docker compose restart` 不会升级镜像。

## 启动

在仓库根目录执行：

```sh
# 需要 go.mod 指定的 Go 工具链及项目锁定的 Flutter 3.41.5。
# Flutter 不在 PATH 时，可设置 VIBERMATE_FLUTTER_BIN 为 flutter 的绝对路径。
bash tool/docker/build-local.sh
docker compose up -d --wait --wait-timeout 90
docker compose ps
docker compose logs --tail=50 vibermate
```

如果桌面版 ViberMate 已占用 `9666`，可在仓库根目录的 `.env` 设置
`VIBERMATE_PORT=9667`，再运行上述启动命令。当前本机采用此设置，访问地址为
**https://localhost:9667**；下文客户端命令中的端口也应改成 `9667`。
`.env` 已被 Git 忽略，Compose 会自动读取并在后续重建时沿用该端口。

默认配置的浏览器地址为 **https://localhost:9666**。全新数据卷首次启动生成一套统一 Runtime
Root CA，同时用于签发 Server HTTPS 证书和授权 AI 流量代理的域名证书。浏览器在信任
该 CA 前会提示证书警告。核对服务端证书的 SHA-256 指纹与启动日志中的 `tlsFingerprint`
一致后继续；统一根指纹见 `runtime_root_ca_loaded caFingerprint=...`。
客户端只需信任这一套 Root CA，但仍须正常验证叶证书的访问地址和有效期。

取得初始化/恢复密钥：

```sh
docker compose exec vibermate /opt/vibermate/vibermated server recovery-key --data-dir /data
```

在网页输入密钥，自行设置所有者用户名和密码。此密钥属于敏感信息，不要写入镜像、
Compose 或提交到仓库。所有者设置完成后可在工作台添加上游账号、发布 Environment。

## 统一 Runtime Root CA

**设置 → 安全与数据 → 统一 Runtime Root CA** 只显示根证书的指纹、有效期，
并提供“下载统一 Root CA”按钮。下载文件为 `vibermate-ca.crt`（PEM 格式），
仅含一张公开根证书，不含私钥，也不是根证书与叶证书的打包文件。

核对页面指纹与服务端启动日志 `runtime_root_ca_loaded caFingerprint=...` 一致后，
在客户端导入并信任。下载不会自动安装信任，也不会绕过浏览器首次连接的 TLS 校验。
这里统一的是签发根；HTTPS 叶证书与 AI 流量域名叶证书仍是不同的证书。

网页不再展示或下载 HTTPS 叶证书，也不再提供 IP / 域名输入、生成待生效证书、
应用证书或在线热切换。对应的旧 API 已移除，不能通过直接调用接口继续换证。
保留的只读接口是 `GET /api/v1/server/root-ca`，使用管理 read token，返回
`vibermate-runtime-root-ca-v1`：公开 `certificatePem`、`fingerprint`、
`notBefore` 和 `notAfter`，不返回 HTTPS 身份或待生效状态。

### 已有部署与证书持久化

**升级不会更换正在使用的 Root CA 或 HTTPS 叶证书，也不会删除数据卷中的证书文件。**
当前数据卷已有的 HTTPS 地址（SAN）、私钥和指纹会继续使用，重启时不会被默认地址覆盖。
旧版 `server-tls-pending.json` 不再读取或应用，原文件保留不动，避免破坏回滚材料。

统一 Root 的唯一持久化来源为 `/data/local-ca/`：

- `root-certificate.pem`：公开根证书，也是下载内容的来源。
- `root-key.pem`：Root 签名私钥，不能提供给客户端。
- `root-manifest.json`：根证书身份与修订。

`/data/server-transport/server-tls-identity.json` 保留 HTTPS 叶证书、SAN 与叶私钥。
历史 `server-tls-ca.json.retired`、`*.before-unified-root`、
`server-tls-identity.json.previous` 可能包含旧私钥，必须和完整数据卷一起保护。
新部署不会创建独立 HTTPS CA、待生效文件或在线换证备份。

从同一 Runtime 导出的旧 `vibermate-traffic-ca.pem` 与 `vibermate-ca.crt`
内容及指纹相同，不需要因为文件改名重复信任。不同 Runtime / 数据卷（例如 macOS
本地 Runtime 与 Docker Runtime）仍有不同的根，不能互相替代。

### HTTPS 地址与部署配置

HTTPS 服务本身仍保留。全新数据卷启动时，会自动用统一 Root 签发内置 HTTPS 叶证书，
默认覆盖 `localhost`、`127.0.0.1` 和 `::1`。需要其他地址时，可由部署者在首次启动
的命令参数中提供 `--tls-hosts 192.168.1.20,vibermate.example.test`；
此参数只初始化尚不存在的身份，不会修改已有证书。本 Compose 默认没有设置该参数。

证书中的地址必须是客户端 URL 实际使用的服务端宿主机 IP / 域名，
不是容器内部 IP 或客户端自身 IP。域名还须正确解析到宿主机。
CA 信任不能解决 SAN 不匹配、DNS、端口映射或防火墙问题。

对于已由统一 Root 签发并覆盖访问地址的服务，可验证：

```sh
curl --cacert ./vibermate-ca.crt https://localhost:9667/api/v1/server/web-auth
```

若已有 HTTPS 身份是历史自签名证书或由旧 CA 签发，下载当前 Root 不会改变它的签发链；
仍需对应的既有信任，或由部署者维护 TLS 身份。网页已没有迁移/换证入口。
固定叶证书指纹的客户端仍须在叶证书变更后核对并更新指纹；导入 CA 不会绕过固定指纹检查。

使用 `tls_files` 模式时，由部署者管理 SAN、签发链和续期，并通过
`--tls-cert` / `--tls-key` 指定外部文件，重启后加载。不要同时使用 `--tls-hosts`。
页面下载的仍是本 Runtime 的 Root CA，可用于其授权 AI 流量代理；
它不自动信任由其他机构签发的外部 HTTPS 证书，外部 HTTPS 的 CA 应从对应签发机构取得。
HTTP 模式也可以下载 Runtime Root CA，但下载不会把 HTTP 服务切换为 HTTPS。

统一 Root 有效期为 10 年，内置 HTTPS 叶证书为 1 年，流量域名叶证书为 24 小时，
均不超过 Root 有效期。当前没有自动 HTTPS 续期；叶证书到期前须由部署者维护并重启服务。
更换 Root 会影响 HTTPS 和流量代理信任；macOS 原有更换根证书流程仍保留，但不会
自动重签已有 HTTPS 身份。始终备份完整数据卷，不要仅为刷新页面而删除证书或私钥。

## 客户端接入

客户端机器需要安装对应平台的 `vibermate` 命令及 Claude Code/Codex。macOS 用户
可使用 App 提供的“设置终端命令”。本机 Docker Server 应通过 `--server` 接入：

```sh
vibermate login --server https://localhost:9666
vibermate doctor --server https://localhost:9666
vibermate run --server https://localhost:9666 -- claude
# 或：
vibermate run --server https://localhost:9666 -- codex
```

使用集中配置的上游账号时，增加 `--env <环境ID>`。未指定 Environment 时，默认
透明捕获会保留客户端原来的服务商、账号和模型。Server 自动发现的地址可能包含
容器内部 IP；客户端应使用宿主机实际发布的地址，当前本机部署为 `https://localhost:9667`。

## 数据与运行方式

- 容器名为 `vibermate-runtime`，本地修改版镜像为 `vibermate-runtime:0.1.10-local`。
- 命名卷 `vibermate-runtime-data` 挂到 `/data`，保存 SQLite、配置、证据、上游密钥、
  初始化/恢复密钥、Server TLS 身份和流量检查 Root CA。保留整个卷才能维持信任身份。
- 容器使用 UID/GID `10001:10001`，首次创建的命名卷继承 `/data` 的所有权及 `0700`
  权限；改为宿主机绑定目录时，需要预先给目录设置相应所有权和权限。
- 一个数据卷只运行一个 Runtime。SQLite 和 `server.lock` 不支持多个实例共享写入。
- 健康检查仅检查容器内部的 HTTPS 控制接口可用性，不验证上游凭据或真实模型调用。
- 根文件系统只读；只有数据卷和临时目录可写。不需要特权模式、Host 网络或 Docker socket。
- 证据数据库和 Server 上游密钥没有静态加密，应保护 Docker 主机和备份。

## 停止、重建和备份

```sh
docker compose stop
docker compose start
docker compose restart
docker compose logs -f --tail=50 vibermate
```

`docker compose down` 删除容器和项目网络，但保留数据卷。重建后继续使用同一个卷：

```sh
docker compose up -d --wait
```

**不要使用 `docker compose down -v`**，除非确实要删除包括密钥和历史证据在内的全部数据。
备份时先停止服务，备份完整的 `vibermate-runtime-data` 卷，然后启动服务；加密保存备份。
升级前保留旧镜像和数据备份，核对版本的数据库兼容性。更新 Dockerfile 的版本时，也要
同时更新发布包校验值、源码提交标签及 Compose 镜像版本。

## 局域网或 Ubuntu 主机

本配置默认只发布到宿主机回环地址。Root CA 下载页面不会修改 Docker 网络配置或获取 Docker socket。
如果要开放到指定的内网/VPN 网卡，先按上文部署配置准备覆盖该地址的 HTTPS 证书，再在 `.env`
设置端口绑定（此设置不会改变证书地址）：

```dotenv
VIBERMATE_BIND_ADDRESS=192.168.1.10
VIBERMATE_PORT=9667
```

将示例地址替换为宿主机实际的网卡 IP，运行 `docker compose up -d --wait`，并在
Docker/主机网络层限制来源。当前早期版本没有公网加固部署承诺。

在可信域名模式中，可用 Compose override 只读挂载证书目录，并将启动参数改为
`--transport tls_files --tls-cert /certs/fullchain.pem --tls-key /certs/privkey.pem`。
文件必须为普通文件，私钥权限 `0600`，且 UID `10001` 可读；不要直接挂载 Certbot
的符号链接。替换证书后重启服务，并处理客户端的证书指纹变更提示。

同一端口还承载 HTTP CONNECT 代理流量，且管理路由拒绝 Forwarded/X-Forwarded-*。
需要网关时，应采用经过验证的四层透传配置。
