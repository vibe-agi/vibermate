[English](docker.md) · [简体中文](docker.zh-CN.md)

# Docker 部署

容器和原生进程使用同一套账号与证书逻辑。先选场景。每个模板使用独立的具名数据卷，
避免一次试运行意外覆盖另一套 Runtime。

| 场景 | 配置 | 浏览器地址 |
| --- | --- | --- |
| 本机个人使用 | `compose.yaml` + `.env.example` | `http://127.0.0.1:9666` |
| 私网/VPN，无公网域名 | `compose.private.yaml` + `.env.private` | 自定义 hosts 名称或 IP 的 HTTPS |
| 公网域名，自动证书 | `compose.public.yaml` + `.env.public` | `https://域名`（443） |
| 已有公共/企业证书 | `compose.team.yaml` + `.env.team` | 证书覆盖的 HTTPS 域名/IP |

## 构建当前源码

```sh
bash tool/docker/build-local.sh
```

脚本会构建 Server、CLI 和 Web，并生成镜像 `vibermate-runtime:local`。如果 Flutter
不在 `PATH` 中，把 `VIBERMATE_FLUTTER_BIN` 设为它的绝对路径。模板使用
`pull_policy: never`，不会拿一个旧的远程镜像冒充当前代码。

可以用 `node tool/docker/smoke-local.mjs` 在独立的容器和数据卷中验证本机模板。脚本
会选择一个空闲的回环端口，验证 Web 初始化、登录、Proxy CA 和重启后的恢复，最后清理
测试资源。

## 本机：默认模板

```sh
docker compose --env-file .env.example up -d --wait --wait-timeout 90
```

打开 <http://127.0.0.1:9666>。端口冲突时修改 `.env.example` 中的
`VIBERMATE_PORT`。该模板固定发布到宿主机回环地址，不读取远程网卡相关的变量；它不是
远程明文部署。模板同时把 `127.0.0.1:<VIBERMATE_PORT>` 作为客户端访问地址传给
Runtime，因此在 Web 中创建专属代理登录时，不会把容器内部的 `172.x` 地址交给用户。

读取初始化/恢复密钥：

```sh
docker compose --env-file .env.example exec vibermate \
  /opt/vibermate/vibermated server recovery-key --data-dir /data
```

## 私网 HTTPS：hosts 名称或 IP

```sh
cp .env.private.example .env.private
# 编辑 VIBERMATE_ACCESS_ADDRESS 与 VIBERMATE_PRIVATE_BIND_ADDRESS
docker compose --env-file .env.private -f compose.private.yaml \
  up -d --wait --wait-timeout 90
```

`VIBERMATE_ACCESS_ADDRESS` 是客户端实际使用的规范 `host:port`：

- DNS 示例：`vibermate.home.arpa:9666`，并在每台客户端的 hosts 文件中写入
  `192.168.1.20 vibermate.home.arpa`。
- IP 示例：`192.168.1.20:9666`，不需要 hosts 文件。

`VIBERMATE_PRIVATE_BIND_ADDRESS` 是宿主机上用来发布端口的网卡地址，不是容器 IP。

ViberMate 会用同一个私有 CA 生成覆盖该 DNS 名称或 IP 的服务器叶证书。安全地导出公开
CA 证书：

```sh
docker compose --env-file .env.private -f compose.private.yaml exec -T vibermate \
  /opt/vibermate/vibermated server ca-certificate --data-dir /data \
  > vibermate-private-ca.crt
openssl x509 -in vibermate-private-ca.crt -noout -fingerprint -sha256
```

该命令在服务器上直接读取数据卷中的 CA 证书，不会导出私钥；这里算出的指纹就是可信的
参考值。通过可信渠道把证书和指纹交给受管客户端，客户端安装前先核对指纹是否一致。
不要从未受信任的网页反向下载 CA。修改访问名称或 IP 并重启后，会在同一个 CA 下重新
签发叶证书，客户端对 CA 的信任不受影响。这个私有 CA 同时授权当前 Runtime 的 AI 流量
检查能力，不要安装到不受管的设备上。

## 公网域名：自动 HTTPS

```sh
cp .env.public.example .env.public
# 编辑公网域名、联系邮箱和需要的绑定地址
docker compose --env-file .env.public -f compose.public.yaml \
  up -d --wait --wait-timeout 120
```

先让域名解析到服务器，并让公网 TCP 443 到达宿主机的 443 端口。模板把宿主机 443 转发
到容器的非特权端口 9666，并使用 TLS-ALPN-01 验证；容器不需要 root、
`NET_BIND_SERVICE` 或 host network。模板中的 `--acme-agree-terms` 表示你明确同意
签发机构的条款。证书和 ACME 状态保存在 `vibermate-public-data` 数据卷中，续期后
热加载。

首次 TLS-ALPN 申请是异步的。容器在运行，不代表证书已经签发；请查看日志和网页中的
「设置 → 安全与数据 → 连接到服务器」。私网 DNS、IP、通配符和 DNS-01 不在当前自动模式
的支持范围内。

## 已有证书

```sh
cp .env.team.example .env.team
# 编辑访问地址、网卡、证书和私钥的绝对路径
docker compose --env-file .env.team -f compose.team.yaml \
  up -d --wait --wait-timeout 90
```

证书必须覆盖 `VIBERMATE_TEAM_ACCESS_ADDRESS` 中的域名或 IP。两份 PEM 文件以只读
普通文件的形式挂载（不能是符号链接）；私钥权限为 `0600`，且容器 UID 10001 可读。
模板不会创建缺失的路径，也不会写入私钥。替换证书后，用同样的命令加上
`--force-recreate` 重建容器，数据卷会保留。

## 登录与托管运行

先在浏览器实际使用的地址上完成所有者初始化，再创建成员。客户端使用同一个地址，并写明
端口：

```sh
vibermate login --server https://runtime.example.com:443
vibermate doctor --server https://runtime.example.com:443
vibermate run --server https://runtime.example.com:443 -- codex
```

私有 CA 模式下，应先安装 CA（见上文）。公共/企业证书由系统根证书直接验证。如果 CLI
已为该 Server 保存了叶证书指纹，可以在系统根验证已经成功后，显式改用系统根验证：

```sh
vibermate trust --server https://runtime.example.com:443 --system-roots
```

## 数据、安全与回滚

- `/data` 保存数据库、用户、上游密钥、Proxy CA、服务器身份和自动证书状态。请完整
  备份数据卷并加密保存。一个数据卷只运行一个 Runtime。具体做法见
  [备份与恢复](backup-and-restore.zh-CN.md)。
- 根文件系统只读，容器 UID/GID 为 `10001:10001`，不挂载 Docker socket，也不需要
  特权模式。健康检查只证明本机入口存活，不证明浏览器信任或上游模型可用。
- `docker compose down` 会保留数据卷。除非确定要永久删除身份、账号和证据，否则不要
  使用 `down -v`。
- 停止、查看日志、读取恢复密钥和回滚时，都要带上与启动时相同的 `--env-file` 和 `-f`。
- 如需限制可以连接的网络，请使用[部署指南](deployment.zh-CN.md#限制可以连接的网络ip-白名单)中的
  IP 白名单。如果前面的四层负载均衡会隐藏客户端地址，请把 `VIBERMATE_TRUSTED_PROXIES` 设为
  负载均衡自己的地址。
- 外部 HTTP 反向代理可能破坏同一端口上的 CONNECT。需要网关时，请验证四层透传，
  不能只测试网页能否打开。

只渲染并检查四套配置：

```sh
node --test tool/docker/compose.test.mjs
```

完整的原生命令、证书边界与首次信任见 [部署与 HTTPS](deployment.zh-CN.md)。
