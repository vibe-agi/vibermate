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
一致后继续；统一根指纹见 `runtime_root_ca_loaded scope=https_and_traffic caFingerprint=...`。
客户端只需信任这一套 Root CA，但仍须正常验证叶证书的访问地址和有效期。

取得初始化/恢复密钥：

```sh
docker compose exec vibermate /opt/vibermate/vibermated server recovery-key --data-dir /data
```

在网页输入密钥，自行设置所有者用户名和密码。此密钥属于敏感信息，不要写入镜像、
Compose 或提交到仓库。所有者设置完成后可在工作台添加上游账号、发布 Environment。

## 服务端证书 IP / 域名配置

证书地址现在由管理员在 **设置 → 安全与数据 → 服务端 HTTPS 证书** 中管理，
不使用 `.env`。Compose 不再传入 `VIBERMATE_TLS_HOSTS`。

1. 在“服务端 IP / 域名”填写客户端 URL 实际使用的宿主机地址，例如
   `192.168.1.20, vibermate.example.test`，不是客户端或容器内部 IP。
   支持逗号或换行分隔的最多 32 个地址，不填写协议或端口；域名需能解析到宿主机。
2. 点击“生成待生效证书”。此时当前证书保持不变，页面单独显示待生效证书和指纹。
   再次生成将替换待生效证书；多个管理页面通过指纹比较避免相互覆盖。
3. 点击“下载统一 Root CA”，得到 `vibermate-ca.crt`（PEM，只有一张根证书，不是证书包），
   核对页面指纹与启动日志 `runtime_root_ca_loaded ... caFingerprint=...` 一致后，在客户端配置信任。
   已信任同一 CA 的客户端不必重复导入。固定叶证书指纹的客户端仍需更新指纹；
   可另用“下载待生效证书”保存 `vibermate-server.crt`。仅下载文件不会自动安装信任。
4. 点击“应用并立即加载”，确认对所有客户端连接的影响。后端先持久化再在线切换，
   不重启容器；已建立的 TLS 连接可继续使用，新连接按客户端信任策略验证新证书。

**已有数据卷的升级沿用原流量 Root CA，不重新生成根证书，也不自动切换当前 HTTPS 证书。**
原来从本 Runtime 导出的 `vibermate-traffic-ca.pem` 与新的 `vibermate-ca.crt` 内容及指纹一致，
不需要因为改名而重复信任。原 `vibermate-server-ca.crt` 是已退役的 HTTPS 专用 CA，不能替代
统一根证书。不同 Runtime / 数据卷（例如 macOS 本地 Runtime 与 Docker）仍有不同的根。

如果页面提示当前 HTTPS 尚未由统一根签发（旧自签名证书、旧 HTTPS CA 或更换根之前的证书），
先信任统一根，再重新生成并应用 HTTPS 证书。旧 CA 签发的待生效证书会保留供核对，但禁止
直接应用，必须重新生成。迁移或重置 Root 不会跳过 CLI 固定叶指纹校验；指纹更新须经客户端
操作者核对。这里统一的是签发根，HTTPS 叶证书和流量域名叶证书仍各不相同。

服务始终保留 `localhost`、`127.0.0.1` 和 `::1`，用于本机连接与健康检查；应用时还会
检查新证书覆盖当前管理页面的访问地址。外部 TLS 文件模式不允许网页修改或签发。
如果响应丢失，不能直接认定应用失败：先配置信任，再刷新并核对指纹；该页面仍允许
下载已经取得的待生效证书。重新生成和应用均需要 Server Owner 的 write 权限。

配置与证书均在命名数据卷的 `/data/server-transport/` 中：

- `server-tls-identity.json`：当前证书、地址清单（证书 SAN）与私钥。
- `server-tls-ca.json.retired`：旧部署迁移后保留的 HTTPS 专用 CA 备份（包含旧私钥）。
  新版本不再用它签名，也不会在新安装中创建 `server-tls-ca.json`。
- `*.before-unified-root`：为迁移旧 CA 签发身份而保存的原格式身份备份。旧二进制不认识
  新的 `issuerCertificatePem` 字段；回滚时应恢复迁移前的完整数据卷，不能仅更换镜像。
- `server-tls-pending.json`：待生效证书和私钥。应用后可能保留相同内容，启动时会识别为
  已应用，不再次显示为待生效。这使写入后进程意外退出不会回退证书。
- `server-tls-identity.json.previous`：最近一次应用前的身份，供服务器侧恢复。

这些身份和备份文件权限均为 `0600`，包含私钥，**不是提供给客户端的下载文件**。重启或重建容器
会复用数据卷中的身份，不被启动默认值覆盖。为兼容已有 CLI，`--tls-hosts` 仅在尚无身份
的首次启动时作为初始地址；本 Compose 已不使用该参数。

统一 Root 的唯一持久化来源为 `/data/local-ca/`：`root-certificate.pem` 是公开根证书，
`root-key.pem` 是签名私钥，`root-manifest.json` 记录身份和修订。HTTPS 模块只保留自己的
叶证书私钥，通过 Runtime 的受限签发接口生成证书，不复制或另存 Root 私钥。

“下载 HTTPS 证书”仍下载当前生效身份的公开部分，不包含私钥。
“下载统一 Root CA”只下载一张公开根证书，可同时用于标准 HTTPS CA 验证和 AI 流量代理信任。
核对相应指纹后，可在支持标准 CA 校验的客户端中指定 CA 文件，例如在生成并应用新证书后：

```sh
curl --cacert ./vibermate-ca.crt https://192.168.1.20:9667/api/v1/server/web-auth
```

CA 信任不能解决证书地址不匹配、DNS 解析、端口未发布或防火墙问题。
建议将 CA 信任限制在需要连接此 Runtime 的客户端；只有确认信任本服务的 CA 管理者后，
才将其导入系统信任库。当前 ViberMate CLI 仍固定首次连接的叶证书指纹，导入 CA
不会绕过其固定指纹检查；本功能不修改 CLI 的信任边界。

统一 Root 有效期为 10 年，HTTPS 叶证书为 1 年，流量域名叶证书为 24 小时，均不超过
根证书的有效期。HTTPS 叶证书到期前须重新生成并应用；当前没有自动 HTTPS 续期。
更换 Root 同时影响 HTTPS 和流量代理信任。macOS 原有“更换根证书”流程会在重启时
换根；旧 HTTPS 身份暂时保留，之后须重新生成并应用，旧待生效证书不得直接沿用。
下载不包含任何私钥，也不会自动安装信任；始终备份完整数据卷。
接口 `GET /api/v1/server/certificate` 使用管理 read token，返回当前身份、可选 pending 身份、
统一 `ca` 公开证书（schema：`vibermate-runtime-root-ca-v1`）及 `issuedByCA` 状态；
`POST /api/v1/server/certificate/stage` 与 `/apply` 使用管理 write token。
HTTP 模式明确返回无证书，不允许在线签发。
首次访问工作台仍需先由操作者建立信任；下载按钮不会绕过浏览器的首次 TLS 校验。

使用 `tls_files` 模式时由操作者管理 SAN 和证书续期，不设置 `--tls-hosts`；下载入口同样
导出当前已加载的公开证书链，不提供统一 Root 下载按钮；其 HTTPS CA 应从签发机构取得，
该显式外部证书模式不受内置统一签发管理。
更新文件后重启才会加载新证书。

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

本配置默认只发布到宿主机回环地址。证书页面不会修改 Docker 网络配置或获取 Docker socket。
如果要开放到指定的内网/VPN 网卡，先在页面生成并应用覆盖该地址的证书，再在 `.env`
设置端口绑定（证书地址不放在 `.env`）：

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
