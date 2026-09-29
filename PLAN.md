# ViberMate 当前实施计划

任务状态以 official-plane 的 ViberMate 项目（`779ab6f7-24dd-49ca-9b01-d8be8ef20a51`）为准：总任务 **VIBERMATE-48**，子任务 49–58。本文件是仓库内的实施索引。

## 目标

在没有外部用户的前提下，把 v0.1.16 之后的代码收敛为干净、正确的单一实现：修复独立审查确认的缺陷，删除兼容与历史机制，文档与代码一致，然后重新发布。

## 已定的架构决定

- **协议边界**：每个进入编解码器的 JSON 文档（请求体、响应体、每个 SSE 事件）先按 `encoding/json` 的大小写折叠规则拒绝重名键，保证代理与客户端看到同一组字段。
- **同方言转发**：原始报文是权威；投影只观察它能建模的部分。未建模的历史、工具定义与字段不阻断请求。上游未建模的输出项是"未证实动作"（`provider_action`），由 Environment 工具策略决定：观察放行、审查询问、严格拒绝。在上游已执行完的托管工具结果按不透明数据透传。
- **审计可用性**：保留"无审计不外发"。终态审计写失败时保留并重试，期间拒绝新外发，写入后自动恢复；关停时仍有未写终态则停止失败。
- **写事务有界**：共享写连接上的清理、维护按 100 条分批提交。
- **Schema**：只有一份 `schema.sql` 和整数修订号，不做迁移与离线转换（[ADR 0021](docs/adr/0021-one-schema-revision-without-migrations.md)）。其他修订创建的数据目录会被拒绝打开且不被修改。
- **Server 授权**：Runtime User 只能启动 Owner 显式授予的 Environment，新成员默认没有授权；"全部 Environment"是显式标志；Owner 自己的会话拥有全部。代理只接受 ASCII 规范主机，策略、审计与拨号使用同一个主机。

## 本分支已完成（`fix/post-016-review`）

- 协议：大小写折叠重名键拒绝；Responses 与 Anthropic 未建模输出走工具策略；Responses 字符串 input、无 type 消息、未知历史/工具/tool_choice/include/input_file 不再被拒；incomplete 保留原因；Anthropic ping 计为存活；转换脚本不能以任何大小写形式改 model。
- 安全：Unicode 主机名绕过 deny 规则；成员默认授权；OAuth 凭据替换不再丢弃新修订上的轮换；私有 PKI 只发中间证书链时固定最上层 CA。
- 可靠性：审计终态熔断与幂等写入；手动清理与维护分批。
- 界面：Route 账号集合只做结构校验并显示真实不可用原因；报表导航原子提交，翻页不跳页、下钻失败保留原视图、错误可重试。
- 结构：Schema 单修订号；删除离线转换工具、开发库归档函数与历史文档。

## 待完成

1. 撤下 GitHub 上 v0.1.14–v0.1.16 的 Release 与 tag，并撤下 Homebrew cask（需要有效的 GitHub token）；随后把能力矩阵改为"当前没有已发布版本"。
2. Server：启动环境变量改为允许列表（S2）；盲隧道遵守出口策略（S4）；Server 进程内拨号默认仅公网，而不是依赖请求上下文。
3. 大 Capture 删除改为异步分批，删除期间显示"删除中"。
4. Codex 子代理身份合并失败导致会话索引整轮中止（L7）。
5. 前端：额度倒计时在窗口失焦时暂停；`supportsAutomaticRefresh` 未参与账号比较；报表行区固定高度与读屏列头。
6. 账号切换后加密推理内容的处理（P11），需要先设计上游状态范围。
7. 依赖：`golang.org/x/crypto` 升级到 v0.56.0。
8. 文档：CONTEXT、module-map、README、部署文档与代码统一；CI 增加 `dart format` 检查。
9. 验收：Go 全量与 race、Flutter 全量、Flutter Web 构建后用无头 Playwright 走主要界面流程。
