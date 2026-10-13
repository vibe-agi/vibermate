# 阅读器行为修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 关闭 R5/R6，使晚到工具结果可见、协议工具返回不再成为虚假的用户发言。

**Architecture:** 查询工作的生命周期与完成结果缓存分开；默认用户气泡依据真实可读内容建立。保留原有 inspector、位置恢复、图片和按需分页组件。

**Tech Stack:** Flutter 3.41.5、Dart 3.11、现有 ControlApi。

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
### Task 1: R5 可刷新的工具结果查询

**Files:**
- Modify: `ui/flutter_app/lib/features/workbench/conversation_reader.dart`
- Test: `ui/flutter_app/test/conversation_reader_test.dart`
- Modify: `ui/flutter_app/lib/core/i18n/app_copy.dart`（刷新结果文案）
- Test: `ui/flutter_app/test/control_api_http_test.dart`（只有 wire/HTTP 改动时）

**Interfaces:**
- Consumes: `WorkbenchController.loadToolResults(exchangeId, callId, cursor: ...)`、`ToolResultPage` 和 Activity 更新。
- Produces: 私有 `_ToolLookupOutcome`，包含 `List<ToolResultMatch> items` 和 `bool complete`。
- `_findToolResults` 返回 `Future<_ToolLookupOutcome>`，缓存仅接收完成且非空的结果。
- 使用 `(String exchangeId, String callId)` Dart record 键和查询 generation，避免旧响应覆盖新的 selection。
- 成功缓存上限 32 项/4 MiB 正文；closing、Controller/对话切换、删除会丢弃对应工作和结果。

- [ ] **Step 1: 写用户可见的晚到结果 red。**

基于现有 `_ReaderApi` / `host` / `detail` 实际 widget：
```dart
await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
await tester.pumpAndSettle();
await tester.tap(find.byKey(const Key('reader-detail-tab-1')));
await tester.pumpAndSettle();
expect(find.text('Result not found yet.'), findsOneWidget);
api.results['late-return'] = ToolResultPage(items: [
  ToolResultMatch(
    exchangeId: 'later-request',
    block: tool('tool_result', 'late-return'),
  ),
]);
await tester.tap(find.byKey(const Key('reader-close-details')));
await tester.pumpAndSettle();
await tester.tap(find.byKey(Key('reader-tools-${activity.id}')));
await tester.pumpAndSettle();
expect(find.text('MATCHED_TOOL_RESULT'), findsOneWidget);
```
另外验证：面板未关闭而新 Activity 到来、分页 RPC 未完成时关闭/重开、较晚的旧响应、记录删除。断言真实显示结果/选中项/位置，不只断言 fake 被调用。

- [ ] **Step 2: 跑 red。**

```sh
cd ui/flutter_app
flutter test --no-pub test/conversation_reader_test.dart --plain-name 'late tool result' --reporter expanded
```
Expected：当前重开仍没有匹配结果。

- [ ] **Step 3: 实现查询工作、完成结果与 generation。**

```dart
final class _ToolLookupOutcome {
  const _ToolLookupOutcome(this.items, {required this.complete});
  final List<ToolResultMatch> items;
  final bool complete;
}
```
分页循环在找到结果或 nextCursor 为空时才 complete；因关闭/切换提前结束时 complete=false。空/失败/不完整 outcome 不进入完成缓存。当前 inspector 的 Future 身份保持稳定，关闭/切换、显式刷新或 relevant Activity 更新时才释放工作引用并失效 generation；不能在 build 中反复创建新 Future 导致请求循环。

核心消费规则：
```dart
if (outcome.complete && outcome.items.isNotEmpty &&
    generation == _toolLookupGeneration) {
  _rememberToolResult(key, outcome.items);
}
```
私有 `_rememberToolResult` 按实际保留 text/arguments/cursor 估算字节，LRU 删除直到满足 32 项和 4 MiB 两个预算。不要用 raw originalSize 冒充实际缓存体积。空结果保留“暂未找到结果”，加简明刷新动作；新 Activity 自动触发后无需用户操作。

- [ ] **Step 4: 验证位置、生命周期和容量。**

```sh
cd ui/flutter_app
flutter analyze --no-pub
flutter test --no-pub test/conversation_reader_test.dart --reporter expanded
CHROME_EXECUTABLE='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' flutter test --no-pub --platform chrome test/conversation_reader_test.dart --reporter expanded
```
两种语言、390px 与大字体场景保留。大量连续查看工具的测试必须验证真实 eviction 后可重新查询，而不是只断言私有常量。搜索页/历史页 API 契约不变。

- [ ] **Step 5: 独立 UI 生命周期 review 后提交。**

提交：`fix: refresh late and interrupted tool result lookups`。
review 核对 incomplete 被当作 empty success、成功缓存大小、旧 Future 响应和对话切换。

### Task 2: R6 人类输入与 wire-user 工具返回

**Files:**
- Modify: `ui/flutter_app/lib/features/workbench/conversation_reader.dart`
- Modify: `ui/flutter_app/lib/core/i18n/app_copy.dart`（中性加载入口有需要时）
- Test: `ui/flutter_app/test/conversation_reader_test.dart`
- Test: `internal/runtimepersistence/reading_content_test.go`（与 R3 的退回/deferred 契约）
- Docs: `docs/capability-support.md`

**Interfaces:**
- Consumes: R3 已校验的 reading messages、现有 `_readerInput` / deferred blocks。
- Produces: 仅可读人类文本/附件进入用户气泡；纯工具返回进入既有处理过程；未证明类型的 deferred 内容使用中性加载入口。
- `_ContentPageScope` 和 `_ContentPageButton` 保持现有 exchange/cursor 约束。

- [ ] **Step 1: 写纯工具返回的 red 与混合输入对照。**

fixture 设置 reading page/inputAnchor，唯一 wire-user block 是 tool_result：
```dart
api.details[activity.id] = detail(
  base,
  [ExchangeContentMessage(
    role: 'user',
    blocks: [tool('tool_result', 'claude-return')],
    agent: null,
  )],
  [block('text', 'DONE')],
  checkpoint: true,
  inputAnchor: List.filled(64, 'c').join(),
);
await tester.pumpWidget(host(controller, [activity]));
await tester.pumpAndSettle();
expect(find.text('User'), findsNothing);
expect(find.byKey(Key('reader-tools-${activity.id}')), findsOneWidget);
```
加入 tool_result+真实文字、tool_result+图片、仅 deferred 未知类型、真实两图片问题、重复提问/重放五类用户行为。用于 label 的 expected 从产品语义独立确定。

- [ ] **Step 2: 跑 red。**

```sh
cd ui/flutter_app
flutter test --no-pub test/conversation_reader_test.dart --plain-name 'wire tool return' --reporter expanded
```
Expected：当前纯工具返回仍显示 User 标题。

- [ ] **Step 3: 按筛选后内容决定呈现，保持工具与 unknown 的来源。**

在 request build 中计算一次：
```dart
final inputBlocks = _readerInput(inputs);
final humanBlocks = inputBlocks.where((block) =>
  block.deferred == null &&
  (block.kind == 'text' || block.kind == 'refusal' ||
   block.providerKind == 'input_image')).toList(growable: false);
```
仅 `humanBlocks.isNotEmpty` 时创建用户气泡；未知 deferred 则用既有中性 ContentPageButton/内容入口，不能画空白 User。混合内容的先后关系仍依据原始有序 messages。工具 blocks 不从处理过程移除；类型一旦在 content page 被证明，展开按真实 tool_result/text 呈现。

不要凭 role='user'、时间或工具名推断人新说了一句话。若 R3 已在后台明确标记某项为未验证，中性呈现必须保持该不确定性。

- [ ] **Step 4: 验证与 R3/R5 共用文件后的集成。**

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/runtimepersistence ./internal/desktopcontrol
cd ui/flutter_app
flutter analyze --no-pub
flutter test --no-pub test/conversation_reader_test.dart --reporter expanded
```
390px/200% 字体下可打开 deferred 页面与图片，返回保持位置。纯工具返回无 User；混合实际文字只显示一次且工具结果仍可查。

- [ ] **Step 5: 独立行为 review 后提交。**

提交：`fix: distinguish human input from protocol tool returns`。
更新能力说明，限定已实测图片和客户端范围；不以缺少 block/body 推断工具执行状态。
