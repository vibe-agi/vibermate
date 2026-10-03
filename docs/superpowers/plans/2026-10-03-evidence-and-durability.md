# 证据与持久化修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 关闭 R3/R4/R7，保持冻结记录校验、writer 在途索引边界和凭据耐久提交。

**Architecture:** 复用现有 transcript 摘要、批处理提交和文件 Store CAS。共享元数据校验只提取实际需要的规则，目录同步失败形成明确的不可用状态。

**Tech Stack:** Go 1.26.8、SQLite revision 1、原有文件 SecretStore。

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

### Task 1: R3 reading 路径校验冻结链

**Files:**
- Modify: `internal/runtimepersistence/reading_content.go`
- Modify: `internal/runtimepersistence/exchange_content_page.go`
- Create: `internal/runtimepersistence/verified_content_nodes.go`
- Test: `internal/runtimepersistence/reading_content_test.go`
- Test: `internal/runtimepersistence/exchange_content_page_test.go`
- Docs: `docs/storage-and-read-performance.md`

**Interfaces:**
- Consumes: `storedContentReference`、`storedTranscriptNode`、`transcriptNodeDigest(parent, messageDigest string) string`。
- Produces: 私有 `validateContentNode(node storedTranscriptNode, expectedDigest string, expectedDepth int) error`。
- `readingUserGroup` 的返回签名保持不变；使用同一规则的 full/read paths 都返回 `exchangecontent.ErrInvalidEvidence`。
- 不新增列、digest 算法或外部 wire 字段。

- [ ] **Step 1: 复制已验证的损坏节点复现，并增加缺失/循环/深度/角色用例。**

沿用 `openTestStore`、`transcriptContentRecordFixture` 和实际 Repository。先写 source-a/source-b 两个合法记录，再只改 source-a 的节点引用。核心现有复现：
```go
a, err := loadStoredContentReference(ctx, store.database, "source-a", at)
if err != nil { t.Fatal(err) }
b, err := loadStoredContentReference(ctx, store.database, "source-b", at)
if err != nil { t.Fatal(err) }
_, err = store.database.ExecContext(ctx,
    `UPDATE runtime_exchange_content_transcripts
     SET message_digest=(SELECT message_digest
       FROM runtime_exchange_content_transcripts WHERE digest=?)
     WHERE digest=?`, b.requestRoot, a.requestRoot)
if err != nil { t.Fatal(err) }
_, err = repo.GetPagedProjection(ctx, "source-a", at, exchangecontent.RequestViewReading)
if !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
    t.Fatalf("corrupted reading accepted: %v", err)
}
```
第二组把 role/parent/depth 分别改错；循环用 context 截止避免旧实现挂住，修复后应直接失败而非等超时。完整路径作独立对照，不以 reading 本身计算 expected。

- [ ] **Step 2: 跑 red。**

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/runtimepersistence -run 'Reading.*(Reject|Integrity|Cycle|Preserves)' -count=1 -v
```
Expected：当前 reading 接受 source-b 或等到 context 超时。

- [ ] **Step 3: 提取共用节点规则，保持各 consumer 完整校验。**

```go
func validateContentNode(node storedTranscriptNode, expectedDigest string, expectedDepth int) error {
    parent := ""
    if node.parentDigest != nil { parent = *node.parentDigest }
    if expectedDepth < 1 || node.depth != expectedDepth || node.digest != expectedDigest ||
        !validStoredDigest(node.messageDigest) ||
        (node.depth > 1 && !validStoredDigest(parent)) ||
        (node.depth == 1 && node.parentDigest != nil) ||
        transcriptNodeDigest(parent, node.messageDigest) != node.digest {
        return exchangecontent.ErrInvalidEvidence
    }
    return nil
}
```
从 current full `contentNodes` 迁移规则至该函数，保留行数/末尾检查。reading 的 recursive walk 加冻结 requestCount 上限，逐步验证 expected root/depth，不能在 SQL 排序收集完无限循环后才检查。

SQL role 只有经消息一致性验证后可参与选择。当前 message digest 来自整个 canonical message JSON；不要误把 block manifest 摘要当成相同算法。若某条角色验证超过默认页预算，使用现有 deferred/中性加载入口，不宣称未证明的用户组；不自动 hydrate 整段历史。

- [ ] **Step 4: 验证合法长历史和读取预算。**

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/runtimepersistence ./internal/exchangecontent ./internal/desktopcontrol
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test -race -short ./internal/runtimepersistence ./internal/exchangecontent
```
断言图片+问题组、相同问题的新位置、重放 anchor 不变、24 条/128 KiB预算、旧 full/page 契约均正确。使用现有性能夹具比较必要元数据遍历，记录性能而不是降低完整性。

- [ ] **Step 5: 独立 review 后提交。**

提交：`fix: verify frozen transcript ancestry in reading views`。
review 检查所有退出路径、坏 role 的选择影响、循环上限与是否意外读取完整正文。

### Task 2: R4 释放已耐久的 writer 索引

**Files:**
- Modify: `internal/rawevidence/manager.go`
- Test: `internal/rawevidence/manager_test.go`
- Test: `internal/runtimepersistence/raw_evidence_repository_test.go`

**Interfaces:**
- Consumes: 成功 `AppendBatch` 的 `[]StoredEnvelope` 与 admission 锁下的两个 watermark map。
- Produces: 私有 `releaseDurableFences(records []StoredEnvelope)`。
- `Observe`、`Flush`、`ListExchange`、`PrepareTerminalScope` 的公开签名不变。

- [ ] **Step 1: 以未打开 UI 的实际 writer 行为写 red。**

用现有 memory repository 输入 5000 个唯一 Exchange，完成 `Flush`。测试断言索引不再持有已提交的请求，而不是只检查 QueueRecords。
```go
if err := manager.Flush(ctx, Watermark{}); err != nil { t.Fatal(err) }
manager.admissionMu.Lock()
remaining := len(manager.exchangeLatest)
manager.admissionMu.Unlock()
if remaining != 0 {
    t.Fatalf("durable unread exchanges retained %d read fences", remaining)
}
```
另用 `blockingAppendRepository` 控制同一 Exchange 的新 admission 竞争，验证 newer watermark 保留；提交失败应保留 fence。

- [ ] **Step 2: 跑 red。**

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/rawevidence -run 'Durable.*Fence|Unread.*Exchange|Newer.*Watermark|Failed.*Commit' -count=1 -v
```
Expected：5000 已落盘请求仍保留 5000 个 fence。

- [ ] **Step 3: 在成功提交路径有界清理，不全表扫描 map。**

```go
func (manager *Manager) releaseDurableFences(records []StoredEnvelope) {
    manager.admissionMu.Lock()
    defer manager.admissionMu.Unlock()
    for _, record := range records {
        if latest := manager.exchangeLatest[record.ExchangeID]; latest <= record.Watermark {
            delete(manager.exchangeLatest, record.ExchangeID)
        }
        key := scopeKey{kind: record.ScopeKind, id: record.ScopeID}
        if latest := manager.scopeLatest[key]; latest <= record.Watermark {
            delete(manager.scopeLatest, key)
        }
    }
}
```
`StoredEnvelope` 当前直接包含 `ScopeKind`、`ScopeID` 字段。函数只在 `AppendBatch` 成功后调用，stats 锁释放后再取得 admission 锁，避免倒序锁。较新的索引不能被旧 batch 移除。

- [ ] **Step 4: 验证 100000 请求、竞争与生命周期。**

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/rawevidence ./internal/runtimepersistence
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test -race -short ./internal/rawevidence
```
十万条容量用例用丢弃已提交 payload 的测试 repository 或真实临时 SQLite；不能把 memoryRepository 自己保存的全部 records 当成 writer 内存泄漏。记录 flush 后 map 大小、队列和 slot；测试 Capture 终态后迟到记录仍被拒绝。

sealed scope tombstone 单独测量。若需要进一步回收，先证明现有持久终态/过期屏障覆盖可能迟到的 admission；直接删除 tombstone 不符合本任务。

- [ ] **Step 5: 独立 concurrency review 后提交。**

提交：`fix: release durable raw evidence read fences`。
review 必须核对锁顺序、batch 失败、新 observation 竞争与 read-after-write。

### Task 3: R7 Server SecretStore 耐久提交

**Files:**
- Modify: `internal/hostsecret/development_file.go`
- Modify: `internal/hostsecret/development_replace_nonwindows.go`
- Modify: `internal/hostsecret/development_replace_windows.go`（仅经核对必要时）
- Create: `internal/hostsecret/secret_file_commit.go`
- Test: `internal/hostsecret/server_file_test.go`
- Test: `internal/hostsecret/development_file_test.go`
- Docs: `docs/backup-and-restore.md`、`docs/backup-and-restore.zh-CN.md`

**Interfaces:**
- Consumes: `persistDevelopmentFile(path, schema string, items map[string]developmentSecret) error` 现有私有写路径。
- Produces: 每实例私有 `secretFileOps` seam 和 `secretCommitError`（含 renamed 状态）。
- Public `secretstore.Store` 接口、schema 和 Reference 不变；外部失败分类仍包含 `secretstore.ErrUnavailable`。
- Store 的 read/inspect/replace/delete 共享提交不确定后的不可用状态。

- [ ] **Step 1: 写真实文件加阶段故障注入的 red 用例。**

通过每实例 ops 控制临时文件 write、file Sync、rename、directory Sync；全程使用合成 `secret://provider/account` 与临时目录。

必须断言：rename 前失败旧值仍可读；rename 后 sync 失败更新不能报告成功、旧缓存不能继续作为当前凭据发放；重新打开后只见完整 JSON；Delete 使用同一规则。对 public consumer：
```go
if !errors.Is(replaceErr, secretstore.ErrUnavailable) {
    t.Fatalf("uncertain commit classified as success: %v", replaceErr)
}
if _, err := store.Read(ctx, reference); !errors.Is(err, secretstore.ErrUnavailable) {
    t.Fatalf("uncertain store issued cached credential: %v", err)
}
```
控制点放在真实文件操作边界，不 mock Store.Replace 本身。

- [ ] **Step 2: 跑 red。**

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/hostsecret -run 'Server.*(Durable|DirectorySync|Uncertain)|Secret.*Commit' -count=1 -v
```
Expected：当前缺少目录同步调用，或 post-rename 失败仍允许缓存发放。

- [ ] **Step 3: 补 Unix 目录持久化和提交状态。**

真实 Unix barrier：
```go
func syncSecretParent(path string) error {
    directory, err := os.Open(filepath.Dir(path))
    if err != nil { return err }
    return errors.Join(directory.Sync(), directory.Close())
}
```
write → fileSync → close → rename → directorySync 的任何失败返回受控错误。rename 已成功后的错误携带内部 renamed 状态。Replace/Delete 在锁下将 Store 标为不可用，后续 read/inspect/replace/delete 返回 ErrUnavailable，不能用旧 map 当作最新值。保留旧 in-flight lease 的原有复制/销毁生命周期。

seam 只放每个 Store 实例内；不新增全局测试开关。Windows 当前使用 `windows.Rename`，须核对平台契约，不能假定已经有 write-through；保留编译兼容并准确限定已验证平台。

- [ ] **Step 4: 跑正常 CAS/重开/并发与故障回归。**

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/hostsecret ./internal/secretstore ./internal/provideraccount ./internal/codexoauth
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test -race -short ./internal/hostsecret ./internal/codexoauth
DEVELOPER_DIR=/Library/Developer/CommandLineTools GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags vibermate_native_secrets ./...
```
验证成功才发布新 revision、失败不会清除当前已确认值；诊断/测试 report 不含秘密值。

- [ ] **Step 5: 独立持久化 review 后提交。**

提交：`fix: make server credential file commits durable`。
不得仅用“进程重开能读到”替代同步失败和提交不确定性证明。
