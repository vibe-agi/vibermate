# Docker 部署

容器和原生进程使用同一套账号与证书逻辑。先选场景，每个模板使用独立、具名数据卷，
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

脚本构建 Server、CLI 和 Web，生成 `vibermate-runtime:local`。Flutter 不在
`PATH` 时，将 `VIBERMATE_FLUTTER_BIN` 设为绝对路径。模板使用 `pull_policy: never`，
不会把旧的远程镜像伪装成当前代码。

可用 `node tool/docker/smoke-local.mjs` 在独立容器和数据卷中验证本机模板；脚本选择
空闲回环端口，验证 Web 初始化、登录、Proxy CA 和重启恢复后清理测试资源。

## 本机：默认模板

```sh
docker compose --env-file .env.example up -d --wait --wait-timeout 90
```

打开 <http://127.0.0.1:9666>。端口冲突时修改 `.env.example` 的
`VIBERMATE_PORT`。模板固定发布到宿主机回环，忽略远程网卡变量；它不是远程明文部署。
模板同时把 `127.0.0.1:<VIBERMATE_PORT>` 作为客户端访问地址传给 Runtime，Web 中创建
手动代理登录时不会把容器内的 `172.x` 地址交付给用户。

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

- DNS 示例：`vibermate.home.arpa:9666`，在每台客户端 hosts 文件写入
  `192.168.1.20 vibermate.home.arpa`。
- IP 示例：`192.168.1.20:9666`，无需 hosts 文件。

ViberMate 会用同一个私有 CA 生成覆盖该 DNS/IP 的服务器叶证书。安全导出公开 CA：

```sh
docker compose --env-file .env.private -f compose.private.yaml exec -T vibermate \
  /opt/vibermate/vibermated server ca-certificate --data-dir /data \
  > vibermate-private-ca.crt
openssl x509 -in vibermate-private-ca.crt -noout -fingerprint -sha256
docker compose --env-file .env.private -f compose.private.yaml logs vibermate
```

核对导出指纹与日志 `caFingerprint`，再通过可信渠道安装到受管客户端。不要通过未信任
的网页反向下载。修改访问名称/IP并重启会在同一 CA 下重签叶证书，客户端 CA 信任不变。
此私有 CA 也授权当前 Runtime 的 AI 流量检查能力，不要安装到不受管设备。

## 公网域名：自动 HTTPS

```sh
cp .env.public.example .env.public
# 编辑公网域名、联系邮箱和需要的绑定地址
docker compose --env-file .env.public -f compose.public.yaml \
  up -d --wait --wait-timeout 120
```

先让域名解析到服务器，并让公网 TCP 443 到达宿主机 443。模板把宿主机 443 转发到
容器非特权端口 9666，使用 TLS-ALPN-01；容器无需 root、`NET_BIND_SERVICE` 或 host
network。`--acme-agree-terms` 是显式条款确认。证书和 ACME 状态保存在
`vibermate-public-data`，续期后热加载。

初次 TLS-ALPN 申请是异步的。容器运行不表示证书已经签发；查看日志及网页中的
「安全与数据 → 连接到服务器」。私网 DNS、IP、通配符与 DNS-01 不属于当前自动模式。

## 已有证书

```sh
cp .env.team.example .env.team
# 编辑访问地址、网卡、证书和私钥的绝对路径
docker compose --env-file .env.team -f compose.team.yaml \
  up -d --wait --wait-timeout 90
```

证书必须覆盖 `VIBERMATE_TEAM_ACCESS_ADDRESS` 的域名/IP。两份 PEM 以只读普通文件
挂载；私钥为 `0600` 且容器 UID 10001 可读。模板不会创建缺失路径或写入私钥。
证书替换后使用同一命令加 `--force-recreate` 重建容器，保留数据卷。

## 登录与托管运行

先在实际浏览器地址完成所有者初始化，再创建成员。客户端使用同一个地址：

```sh
vibermate login --server https://runtime.example.com
vibermate doctor --server https://runtime.example.com
vibermate run --server https://runtime.example.com -- codex
```

私有 CA 模式应先安装 CA。公共/企业证书由系统根直接验证。CLI 若已有旧叶指纹，可在
系统验证已经成功后显式迁移：

```sh
vibermate trust --server https://runtime.example.com --system-roots
```

## 数据、安全与回滚

- `/data` 保存数据库、用户、上游密钥、Proxy CA、服务器身份及自动证书状态；完整备份
  数据卷并加密保存。一个卷只运行一个 Runtime。
- 根文件系统只读，容器 UID/GID 为 `10001:10001`，不挂载 Docker socket，不需要特权
  模式。健康检查只证明本机入口存活，不证明浏览器信任或上游模型可用。
- `docker compose down` 保留卷；除非明确永久删除身份、账号和证据，不要使用 `down -v`。
- 停止、日志、恢复密钥和回滚都要带启动时相同的 `--env-file` 与 `-f`。
- 外部 HTTP 反向代理可能破坏同端口 CONNECT。需要网关时验证四层透传，不能只测试网页。

只渲染并检查四套配置：

```sh
node --test tool/docker/compose.test.mjs
```

完整的原生命令、证书边界与首次信任见 [部署与 HTTPS](deployment.md)。
