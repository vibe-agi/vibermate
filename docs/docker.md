# Docker 部署

不使用 Docker 也可以运行同一个 Web 工作台；原生进程、本机 App 与容器的选择见
[统一部署与接入指南](deployment.md)。这里仅说明容器的启动、文件挂载和网络映射。

先选使用场景，不需要先理解证书体系：

| 你要做什么 | 使用哪个配置 | 浏览器地址 |
| --- | --- | --- |
| 只在这台电脑使用，没有域名、证书 | compose.local.yaml | http://127.0.0.1:9666 |
| 团队从其他电脑连接，已有服务器证书 | compose.team.yaml 和 .env.team | 证书覆盖的 HTTPS 域名或 IP |
| 已经使用 PR #15 的内置 HTTPS 部署 | 继续使用 compose.yaml 和原数据卷 | 原来的 HTTPS 地址 |

**更新不会自动把已有 HTTPS 改为 HTTP，也不会更换 CA。** 三份配置默认使用不同的
项目名和数据卷；切换模板是新建部署，不是自动迁移。不要为了消除证书警告删除原数据卷。

## 先构建当前代码

这是源码构建配置，还不是一条命令拉取的官方容器发行版。需要 Docker Compose、
go.mod 指定的 Go 工具链，以及项目锁定的 Flutter 3.41.5。仓库根目录运行：

    bash tool/docker/build-local.sh

Flutter 不在 PATH 时，设置 VIBERMATE_FLUTTER_BIN 为其绝对路径。脚本会构建
当前 Server、CLI 和 Web，并生成 vibermate-runtime:0.1.10-local。构建后运行的
才是这份代码；仅更新仓库或执行 docker compose restart 不会升级镜像。

Dockerfile 的 runtime 目标封装官方 v0.1.10 Linux 发布包，固定源码提交为
1adc765bd34331e2115339425e45e4b55190f26e，并校验对应 ARM64/x86-64 发布包的
SHA-256。local 目标在其上覆盖当前源码构建结果。后两份配置禁止自动拉取同名镜像，
避免误把缺少本地修改的镜像当成当前代码。

## 个人：本机容器，无需域名或证书

    docker compose -f compose.local.yaml up -d --wait --wait-timeout 90

打开 **http://127.0.0.1:9666**。若 App 已占用 9666，在 .env 设置
VIBERMATE_PORT=9667，重新执行启动命令，再打开 http://127.0.0.1:9667。
所有客户端命令也应使用这个端口。不要把地址改成容器内部的 172.x.x.x。

第一次使用，在服务器机器上取得初始化/恢复密钥：

    docker compose -f compose.local.yaml exec vibermate /opt/vibermate/vibermated server recovery-key --data-dir /data

将密钥输入网页初始化表单，自行设置所有者用户名和密码。没有默认 admin/admin。
之后使用用户名和密码登录；恢复密钥只用于初始化和账号恢复，不能分享给普通成员，
不要写入镜像或提交到仓库。

客户端电脑安装 vibermate 和 Codex/Claude 后：

    vibermate login --server http://127.0.0.1:9666
    vibermate doctor --server http://127.0.0.1:9666
    vibermate run --server http://127.0.0.1:9666 -- codex
    # 或：
    vibermate run --server http://127.0.0.1:9666 -- claude

**Docker 接入要带 --server**；不带它的本地启动连接的是 App。需要检查并记录对话时，
先在「流量策略」发布自定义策略，再启动：

    vibermate run --server <地址> --env <策略ID> -- codex

默认 System Transparent 是透明转发，只保留无正文的连接证据，不会自动录下对话内容。

这个 HTTP 模板固定发布到宿主机 127.0.0.1，不会读取远程网卡绑定变量。它不是给其他
电脑直连的明文服务；远程使用请选 HTTPS 或可信隧道。HTTP 本身不加密，仍需保护主机和
Docker 网络，不要把不可信容器接入同一网络。建议使用当前受支持的 Docker；Docker 28
以前的版本有回环发布端口可能被同网段访问的限制，见 [Docker 端口发布说明](https://docs.docker.com/engine/network/port-publishing/)。

## 团队：自己的域名和服务器证书

准备一张覆盖实际访问域名/IP 的服务器证书，并确保 DNS、端口映射和防火墙正确。
复制 .env.team.example 为 .env.team，填写宿主机网卡、端口和两份 PEM 文件的绝对路径。
这里只保存路径，不保存私钥内容或账号密码。

    docker compose --env-file .env.team -f compose.team.yaml up -d --wait --wait-timeout 90
    docker compose --env-file .env.team -f compose.team.yaml exec vibermate /opt/vibermate/vibermated server recovery-key --data-dir /data

例如访问 https://runtime.example.com:9666：在该地址完成所有者初始化，再到
「用户管理」添加成员，把网页地址发给对方。每个人登录自己的账号；浏览器用户不需要
安装 ViberMate 的代理 CA。客户端命令使用同一个 HTTPS 地址。

证书文件必须是普通文件，不是 Certbot 符号链接。私钥需为 0600，并可由容器
UID 10001 读取。配置以只读方式挂载文件，不会为错误路径创建空目录，也不会覆盖
你的证书。更新证书后用相同启动命令加 --force-recreate 重建容器，以重新挂载并加载
替换后的文件；保留数据卷。当前没有自动续期或证书热加载。

当前 CLI 使用已有的叶证书指纹固定机制，尚未完成标准公共 PKI 与显式私有 CA 信任模式
的迁移。**不要把首次自动固定指纹当成经过身份验证的安全引导**；首次在可信网络接入，
通过独立可信渠道核对身份。证书续期可能触发身份变化，需要专门处理，不能删掉信任记录
或关闭校验来消除提示。这些是合回稳定分支前的优化验收项，不是已经解决的能力。

此模板使用 ViberMate 原生 TLS。同一个端口还承载 CONNECT，管理路由拒绝
Forwarded / X-Forwarded-*。需要网关时采用经过验证的四层透传；不能默认把普通
Nginx/Caddy HTTP 反代当成完整的 ViberMate 接入代理。

## 两种证书分别做什么

| 连接 | 应该看到什么证书 | 谁负责 |
| --- | --- | --- |
| 浏览器/CLI → runtime.example.com | 覆盖 Runtime 域名/IP 的服务器证书 | 部署者的公共/企业签发机构，或明确配置的私有身份 |
| Agent → CONNECT 内的 api.openai.com 等目标 | 目标域名的叶证书，由此 Runtime 的代理 CA 签发 | ViberMate 的授权流量检查 |
| Runtime → 真正的 AI 上游 | 上游服务自己的证书，严格验证 | 上游服务及所选网络出口 |

所以，团队代理入口使用你提供的服务器证书是正常的；它不应该被拿来当成 CONNECT
内部 AI 域名的证书。下载代理 CA 不会修复域名证书的 SAN、过期或外部签发链问题。

「安全与数据」分别说明服务器连接和 AI 流量检查。展开「手动接入与证书详情」后，
可下载 **vibermate-proxy-ca.crt**。它只包含一张公开 CA，不含私钥。受支持的托管
启动会自动给子进程提供必要信任；只用浏览器无需下载。手动接入时先与服务器日志中的
caFingerprint 核对，不要通过一个尚未信任的 HTTPS 连接盲目取得初始信任。

API 仍为认证后的 GET /api/v1/server/root-ca，保留 vibermate-runtime-root-ca-v1
契约。文件名和页面文案改变不意味着生成了新 CA。旧 vibermate-ca.crt 或
vibermate-traffic-ca.pem 只要来自同一个未换根的 Runtime，其内容和指纹仍相同。

## 已有内置 HTTPS 部署

继续使用原命令、原 .env 和 vibermate-runtime-data 卷：

    docker compose up -d --wait

PR #15 的新内置 HTTPS 身份可能由代理 CA 签发，默认覆盖 localhost、127.0.0.1、
::1。历史身份可能来自不同签发机构；两种用途不能仅凭同一张下载证书混为一谈。
--tls-hosts 仅在首次生成身份时生效，不会修改已有 SAN；设置页没有在线修改域名
或重新签发服务器证书的表单。不要照旧 .env.example 中不存在的页面操作。

内置 HTTPS 叶证书有效期为一年，目前没有自动续期；到期前需部署者维护身份。
代理 CA 更换不会自动重签现有 HTTPS 叶证书。固定叶指纹的客户端也有独立的信任状态。
这些边界会在集成分支继续优化，升级时不得强制轮换现有身份。

## 数据、健康检查和回滚

- 本机、团队、已有 HTTPS 模板的数据卷分别为 vibermate-local-data、
  vibermate-team-data、vibermate-runtime-data。它们是独立 Runtime，不能互信或
  用一个空卷替换另一个卷来完成升级。
- /data 保存数据库、上游密钥、用户、恢复密钥、代理 CA 和可能存在的内置 HTTPS 身份。
  /data/local-ca/ 中的私钥及历史 *.previous / *.retired 备份都必须保护，绝不发给客户端。
- 一个卷只运行一个 Runtime。容器 UID/GID 为 10001:10001，根文件系统只读，
  只允许数据卷和临时目录写入；不需要特权模式、Docker socket 或 host 网络。
- 本地 HTTP 健康检查只验证 Web 认证入口存活。HTTPS 模板的容器内回环探针跳过身份
  校验，也只是存活检查；它不证明浏览器信任、客户端连接或上游模型正常。
- 停止服务后备份整个卷，加密保存。现有数据库和上游密钥没有静态加密。
- 停止/查看日志等命令始终带上启动时相同的 -f 和 --env-file。
  down 保留卷；**不要使用 down -v**，除非明确要删除密钥、用户和证据。
- 回滚保留旧镜像和完整卷备份，并确认数据库兼容性。不要直接把新卷当旧卷恢复。

配置回归检查（只渲染 Compose，不启动容器）：

    node --test tool/docker/compose.test.mjs

后续页面、信任迁移和验收范围见 [接入与信任体验规划](plans/2026-09-21-runtime-setup-and-trust.md)。
