# 权限与客户端修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 关闭 R1/R2，保证自动工具权限只依赖本机证明、实际 Codex 请求进程使用当前 Capture。

**Architecture:** 保留展示路径声明；增加受现有 Workspace evidence 约束的本地权限接口。启动参数兼容性单独处理，不改变认证或 Root 授权。

**Tech Stack:** Go 1.26.8、现有 CLI/代理与测试基础设施。

**Spec:** `docs/superpowers/specs/2026-10-03-production-readiness-design.md`

## Global Constraints

- Go 1.26.8；Flutter 3.41.5@`2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa`；CI Node.js 22.23.1。
- SQLite schema revision 1；不新增 runtime 依赖，不重置现有数据。
- 每项行为修复必须记录失败原因正确的 red→green；review 不能代替测试。
- 代码修改在隔离工作区完成；当前未发布功能必须完整带入基线。
- 英文/简体中文同步；保留原有权限、记录保留、阅读位置和版本兼容性。
- 实机测试使用隔离配置、合成数据或已有明确授权的最小请求；不输出凭据。
- 子任务按控制器账本执行，多个实现者不能同时修改同一工作区。

---

### Task 1: R1 本地文件检查的权限边界

**Files:**
- Modify: `internal/captureadmission/admission.go`
- Modify: `internal/exchange/pipeline.go`
- Test: `internal/captureadmission/admission_test.go`
- Test: `internal/exchange/pipeline_test.go`
- Test: `internal/toolpolicy/gate_test.go`
- Docs: `docs/module-map.md`

**Interfaces:**
- Consumes: `Admission.WorkspaceScope() (workspaceidentity.Scope, bool)`、`Admission.Supports(clientadapter.Feature) bool`。
- Produces: `Admission.LocalWorkspaceRoot() (string, bool)`。
- Existing `WorkspaceRoot() (string, bool)` 继续提供展示/脱敏声明；它不是文件系统权限。
- `Pipeline.decideTools` 的 `ToolDecisionContext` 只能取得本机证明的 root。

- [ ] **Step 1: 将远端误放行复现变成正式消费链回归。**

在已有 `TestToolDecisionRejectsBeforeAnyToolBytesReachClient` 的实际 Pipeline fixture 上构造两种 Admission：本地证据、registered_companion。使用现有 `decisionDouble.lastRequest()` 检查实际传到工具决策的上下文；不是仅调用新增 getter。

核心断言：
```go
decision := decisions.lastRequest()
if root, ok := decision.Context().WorkspaceRoot(); ok || root != "" {
    t.Fatalf("remote workspace acquired local filesystem authority: %q", root)
}
if decision.Context().StructuredWorkspaceTools() {
    t.Fatal("remote scope acquired automatic file-operation capability")
}
```
用合法内置 Claude 2.1.220 evidence 和合成远端 workspace。远端声明目录在服务端存在也必须满足上述断言；另加本地正常文件对照。

- [ ] **Step 2: 跑 red。**

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/exchange ./internal/toolpolicy ./internal/captureadmission -run 'RemoteWorkspace|WorkspaceAuthority' -count=1 -v
```
Expected：当前远端上下文仍含本地 root/结构化权限，断言失败。夹具错误必须先修复。

- [ ] **Step 3: 加入最小权限接口并改实际 consumer。**

```go
func (admission Admission) LocalWorkspaceRoot() (string, bool) {
    scope, available := admission.WorkspaceScope()
    if !available || scope.Evidence() != workspaceidentity.EvidenceLocalLauncher {
        return "", false
    }
    return admission.WorkspaceRoot()
}
```
在 `Pipeline.decideTools` 中使用：
```go
var local bool
workspaceRoot, local = admission.LocalWorkspaceRoot()
structuredWorkspaceTools = local &&
    admission.Supports(clientadapter.FeatureStructuredWorkspaceTools)
```
保留 message-transform 与客户端显示所需的原始路径声明。检查已有本地无 scope 情况，不能替无证明者补权限。

- [ ] **Step 4: 验证实际行为矩阵。**

Review 的远端 Read 应调用原有人工决策；Strict 的远端 Read 应拒绝；Observe 应正常观察；本地已验证安全文件应自动通过。覆盖同名目录、符号链接、目录不存在、Shell/MCP 未证明操作。

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/captureadmission ./internal/exchange ./internal/toolpolicy
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test -race -short ./internal/captureadmission ./internal/exchange ./internal/toolpolicy
```

- [ ] **Step 5: 独立任务 review 后提交。**

提交：`fix: require local workspace evidence for automatic tool approval`。
只 stage 本任务文件，ledger 记录 red、green、BASE/HEAD 和 reviewer 的 spec/quality 结论。

### Task 2: R2 Codex Capture 与实际工作进程绑定

**Files:**
- Create: `internal/runlauncher/codex_isolation.go`
- Create: `internal/runlauncher/codex_isolation_test.go`
- Modify: `internal/runlauncher/launcher.go`
- Test: `internal/runlauncher/launcher_integration_test.go`
- Modify: `cmd/vibermate/main.go`（错误映射）
- Modify: `locales/en-US.json`、`locales/zh-CN.json`
- Test: `internal/runlauncher/invocation_test.go`
- Docs: `docs/capability-support.md`、`docs/deployment.md`、`docs/deployment.zh-CN.md`

**Interfaces:**
- Consumes: 已验证的 executable 路径、用户原始 argv、现有准备后的 argv、caller environment、已知 client family。
- Produces: 私有 `prepareCodexCaptureArguments(ctx context.Context, executable string, original []string, prepared []string, environment []string, legacyIndependent bool) ([]string, error)`。
- `legacyIndependent` 只能由当前有效的 Codex 0.145.0 Adapter evidence 与 LaunchCodexResponsesHTTP recipe 共同导出；helper 还须确认准备参数中该 recipe 的必需非空 config override 前缀，不能由 probe 的自报版本或用户任意 config 参数直接授予。
- New sentinel: `ErrCodexIsolationUnsupported`，由 CLI 映射到本地化说明。
- 只在 catalog 已知 Codex family 的调用点使用；此函数不提高 recognition，不修改 Adapter/Root/Account 权限。
- 新私有 `classifyCodexInvocation(args []string) (codexInvocationKind, error)`；枚举 interactive、separate-process、remote。

- [ ] **Step 1: 先核对真实参数契约并保存元数据证据。**

在隔离配置中查看实际 Codex 0.145.0、0.159.2、0.160.0 的 `--version`、`--help`、`exec --help`、`resume --help`、`app-server --help`，并核对对应官方源码。证据只记录版本、支持的参数和进程模式，不记录登录文件/环境值。若支持入口与本设计不同，先记 controller ruling、修订契约，再写实现。

Controller 已完成上述三版帮助、官方源码 tag→commit 核对；证据在 `.superpowers/sdd/2026-10-03-authority-and-client-lifecycle/codex-0.160-contract-evidence.md`。0.145.0 并非天然独立：其非空 config override 才会禁用已有 daemon 复用，必须绑定已验证 recipe 前缀。所有版本的 app-server 都有 daemon/proxy 子命令，不能把整个 app-server family 当成前台进程。实现者须核对对应源码的实际分支；已有 session 控制命令没有独立证明时失败关闭。

- [ ] **Step 2: 为复用旧后台的触发条件写 red 集成用例。**

使用真实 Launcher 与原有控制/代理 fixture，测试 child 仅连接既有后台、后台继续持有旧 proxy 的假客户端。断言当前 Capture 的请求必须经新 proxy、父进程结束后不会留下新持有其环境的后台。旧后台本身作为独立 fixture，由测试自己清理。

参数分类用 literal cases：
```go
cases := []struct {
    args []string
    want codexInvocationKind
}{
    {[]string{}, codexInteractive},
    {[]string{"resume", "fixture-thread"}, codexInteractive},
    {[]string{"exec", "fixture prompt"}, codexSeparateProcess},
    {[]string{"app-server"}, codexSeparateProcess},
    {[]string{"--model", "exec", "resume", "fixture-thread"}, codexInteractive},
    {[]string{"--", "exec"}, codexInteractive},
    {[]string{"--remote", "ws://127.0.0.1:9999"}, codexRemote},
}
```
加入已有 `--no-daemon` 的幂等用例、help 超时/超量输出、legacy 缺少 recipe/config 前缀拒绝、app-server daemon/proxy 与 agents 拒绝用例。测试 global/嵌套 option values、变长 image 和短参数组合；不能把名为 exec/daemon 的 option 值当命令，也不能把 `--` 后的提示当控制参数。fake help 只替代外部 CLI 进程，Launcher/argv consumer 保持真实。

- [ ] **Step 3: 跑 red。**

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/runlauncher -run 'CodexCaptureIsolation|CodexInvocationClass|CodexHelpBound' -count=1 -v
```
Expected：当前调用允许共享后台继续工作，或没有隔离参数准备方法。

- [ ] **Step 4: 实现独立、限界的能力读取和参数准备。**

为已知 Codex family 使用原生 `--help`，1500 ms 截止、stdout+stderr 合计 64 KiB。探测环境剔除 Capture capability/代理值，stdin 关闭，输出溢出/超时关闭 probe；不得打印原始输出。不要以包级全局变量注入测试 hook。

处理规则：
```text
remote invocation -> ErrCodexIsolationUnsupported
existing-daemon control/proxy, shared-only or unproven mode -> ErrCodexIsolationUnsupported
separate-process invocation -> prepared argv unchanged
interactive + explicit --no-daemon -> prepared argv unchanged
interactive + proven --no-daemon support -> prefix --no-daemon
verified 0.145.0 + verified launch recipe + mandatory prepared config override prefix -> existing argv
other unproven interactive mode -> ErrCodexIsolationUnsupported
```
原始提示、option values、`--` 分隔及 approval/sandbox 参数全部保留。普通前台 app-server 与其 daemon/proxy 子命令分别证明；未知或歧义的入口不能回落到共享后台。能力事实不改变 release 验证和 Root 授权。Actual launcher 在创建 child 前消费准备结果，失败仍用原有 finish cleanup。

核心参数变换（分类与 probe 在本任务内定义）：
```go
if kind == codexRemote { return nil, ErrCodexIsolationUnsupported }
if kind == codexSeparateProcess || explicitNoDaemon {
    return append([]string(nil), prepared...), nil
}
if supportsNoDaemon {
    return append([]string{"--no-daemon"}, prepared...), nil
}
if legacyIndependent && hasVerifiedRecipeConfigPrefix(prepared, original) {
    return append([]string(nil), prepared...), nil
}
return nil, ErrCodexIsolationUnsupported
```

- [ ] **Step 5: 跑行为回归，补用户错误说明。**

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/runlauncher ./cmd/vibermate ./locales
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test -race -short ./internal/runlauncher
```
检查三个版本的正常 exec、interactive、resume、全局 option、已存在后台和退出后下一次 Capture。无法捕获的 remote 模式说明本次策略不能应用到另一个后台，给出独立启动办法。

- [ ] **Step 6: 独立 review 与提交。**

提交：`fix: isolate interactive Codex work within each capture`。
review 必须检查 probe 权限、进程清理、版本兼容、未验证客户端的授权字段及参数分类。MCP 的端点修复与真实客户端完整验收在主计划 Task 8 完成，本任务不以纯文本 OK 代替该验收。
