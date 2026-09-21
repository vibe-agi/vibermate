# v0.1.10 provider_transport_failed 初步诊断

## 基线与范围

- 修复分支：`fix/provider-transport-failed`。
- 基线：`v0.1.10`，提交 `1adc765bd34331e2115339425e45e4b55190f26e`。
- 创建分支时，`origin/m1/root-leaf-foundation` 也指向该提交。
- 未合入 ACP 分支。已有 Docker 部署配置改动保留在工作区。
- v0.1.10 修复压缩 HTTP 正文的显示，发布说明明确未宣称修复连接失败。

## 历史记录

检查的是本机桌面版的已停止、已 checkpoint 的 SQLite 数据库；读取时没有
打开该数据库的进程，也没有 WAL/SHM 文件。查询仅涉及失败元数据，不涉及密钥、
请求正文或原始 ClientHello。它不是当前 Docker Server 的数据库。

2026-09-07 19:57:19–19:57:26（Asia/Shanghai）共有 12 次失败，与用户截图时间匹配：

- 目标：`https://chatgpt.com`。
- Activity：`provider_transport_failed`，`provider_status=0`。
- EgressAttempt：`transport_failed`，耗时 0–7 毫秒，`bytes_in=0`。
- 策略：`follow-client`，`observed-client-strict-h1`。
- `clientOfferedAlpn` 和 `upstreamOfferedAlpn` 均为空，没有成功生效的上游指纹。

`bytes_out` 不能证明请求已上网：`providertransport.Client.Do` 的失败路径直接把
请求正文长度写入该字段。历史表也没有保存底层错误文本，因此这些记录不能独立
区分 DNS、TLS 验证、超时、连接重置或本地参数校验失败。

## 已复现的具体错误

修复前使用过以下显式启用的诊断测试（现已转为下文所列的修复后回归测试）：

```sh
go test -tags=diagnostics \
  -run '^TestDiagnoseV0110ObservedClientWithoutALPN$' \
  -count=1 -v ./internal/transportprofile
```

测试通过内存连接捕获一个不带 ALPN 扩展的有效 TLS ClientHello，使用 v0.1.10
内置 `follow-client` HTTP/1.1 策略调用生产 Connector。替代拨号器禁止网络访问。
实际输出：

```text
no transport fingerprint profile succeeded
observed ClientHello has no supported ALPN
outbound dial attempts=0
```

`prepareObservedSpec` 要求客户端提供的 ALPN 与模板允许的协议存在交集。
当客户端不带 ALPN 时，它在拨号前返回上述错误。内置 observed-client 模板没有
备用模板，Connector 因而返回 `ErrNoTransportProfile`；上层最终把它归为
`provider_transport_failed`。

这是本地 TLS 参数准备失败，尚未执行上游 DNS、TCP 连接或 TLS 握手。
它与这批历史记录高度吻合，但历史数据没有保留底层错误链，不能声称已经读取到
旧请求的实际错误原文，也不能据此排除其他请求具有不同原因。

## 后续修复依据

1. 在受控诊断中保留失败阶段和类型，并关联 Exchange/EgressAttempt；区分本地
   参数校验、DNS、TCP 连接、TLS 验证/握手、响应头超时、流读取超时和连接重置。
   不直接把可能含 URL 参数或敏感内容的任意 `err.Error()` 返回给客户端。
2. 对不带 ALPN 的 HTTP/1.1 客户端补兼容性测试，再决定严格 TLS 和指纹策略的
   修复方式。不能用关闭证书校验或放宽 HTTP/2 协商来掩盖问题。
3. 使用原失败入口重新请求，确认实际底层原因及修复后的响应完整性。

以上为初步诊断时的结论；当时仅创建分支、查证历史元数据并添加诊断测试，
未修改生产连接逻辑或部署修复版。

## 2026-09-09 修复

后续对当前 Docker Server 的只读元数据检查发现相同模式：`follow-client` /
`observed-client-strict-h1`，上游状态码为 0，耗时 0–1 毫秒，没有 ALPN。
关联连接的客户端 TLS 握手已成功。原始错误未落盘，不能声称直接读取到了线上
底层错误文本；本地对照测试确认了上述确定可复现的兼容性缺陷。

本次代码调整：

- 仅 HTTP/1.1 模板允许有效 ClientHello 不携带 ALPN；上游仍不添加 ALPN 扩展，
  握手后要求协商结果为空。携带不兼容协议不等同于未携带 ALPN。
- HTTP/2 仍必须明确协商 `h2`；显式携带 ALPN 的 HTTP/1.1 请求仍要求协商成功。
  继续验证证书链、主机名、SNI 和 TLS 版本，不增加隐式备用模板。
- Connector 在失败出口也保留原有脱敏原因码，例如 `observation_unavailable`、
  `application_protocol_unavailable`，避免全部模板失败或证书校验失败时丢失原因。
  `UsedFallback` 根据实际尝试链判断，不再把单次失败误报为使用了备用模板。
  该调整不等于完整实现 DNS、TCP、TLS、流读取错误的细分类，也不输出任意错误原文。
- 增加无 ALPN 的完整 HTTP 请求和分块流式响应回归测试，并覆盖不兼容 ALPN、
  缺失 SNI、HTTP/2、证书不可信、主机名不匹配及失败证据的 JSON 序列化。

修复后的纯内存诊断命令：

```sh
go test -tags=diagnostics \
  -run '^TestDiagnoseObservedClientWithoutALPNReachesDialer$' \
  -count=1 -v ./internal/transportprofile
```

该测试现在要求无 ALPN 的请求能够到达禁止联网的测试拨号器，而非在参数准备时
被拒绝。实际 TLS 和 HTTP 成功路径由常规测试覆盖，全部使用本地测试服务。

验证结果：完整 `go test ./... -count=1 -timeout=5m` 通过；
`internal/transportprofile`、`internal/providertransport`、`internal/productruntime`
的 `go test -race` 通过。相关模块回归也覆盖了入口代理、Exchange、Activity 和
wire profile；没有向真实 AI 上游发送测试请求。

代码修复阶段未更新 Docker 容器、证书或客户端配置；不能用离线测试成功替代
真实上游请求验证。后续 Docker 部署结果见下节。

## 2026-09-10 UTC Docker 部署

- 已在原数据卷上替换容器，新镜像为
  `sha256:62ec95ee7b211773c8e1978e3dfa37f3c6d48420b2522efc71546b636cfe5a53`。
- 使用 Go 1.25.13 重新编译 Linux ARM64 Server 和 CLI；保留原容器的 Web 资源。
- 在新镜像的隔离容器中，无 ALPN HTTP/1.1、显式 ALPN HTTP/1.1、HTTP/2、
  证书拒绝和分块流式响应回归通过；测试未访问真实 AI 上游。
- 服务保持 `127.0.0.1:9667`，状态 `healthy`；使用原 CA 严格验证，HTTPS 控制接口
  和页面均返回 200。Root CA 与 HTTPS 身份文件的升级前后校验值一致。
- 已保留旧镜像和停服后的一致性数据卷备份，未删除持久化数据。
- 真实模型请求尚未由本次部署流程发起，仍需从原失败入口验证最终响应完整性。
