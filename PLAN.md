# Signed Client Identity Hardening

Status: active
Created: 2026-08-02
Implementation baseline: `1783d4d` (design) / `bfd5df5` (implementation)
Design authority: `docs/adr/0016-signed-identity-client-recognition.md` —
signer authority is Team ID + identifier
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0a-fixed-client-observation.md`

## 现状

ADR-0016 的 signer tier 已接通并可用。但它的安全判定**由字符串包含实现，不由结构实现**：

- `internal/codesignature/signature.go` 的 `Requirement.Valid()` 检查
  `strings.Contains(anchor apple)` 与 `strings.Contains(identifier )`；
- `internal/clientadapter/signer.go` 的 `validateSigner` 检查
  `strings.Contains(subject.OU)`。

因此 `identifier "anchor apple subject.OU"` 这样的字符串——三个词全在字面量里——能通过全部
检查。当前内置的两个条目本身正确且经代码评审，所以不是现成漏洞；风险是**将来一个写错的
catalog 条目会扩大 Root 的交付对象**。注释宣称的结构保证目前强于代码。

## 这一片要做什么

**取消 catalog 持有任意 requirement 字符串的能力。**

Signer 改为持有 typed `SigningIdentifier` 与 `TeamID`；requirement 由 codesignature 从经过严格
校验的字段生成，模板固定，caller 不能提交表达式，也不解析任何 requirement DSL。

## 不变量

1. 生成的 requirement 模板固定包含：identifier、Apple generic anchor、Developer ID
   intermediate 与 leaf 扩展、Team ID。缺一不可，顺序与内容不由 caller 决定。
2. 字段有明确字符集与长度上限，拒绝引号、空白、控制字符、操作符注入与过长值。
3. 不再有任何基于 `strings.Contains` 的安全判定。
4. 结构门禁防止 raw requirement literal 再次进入 catalog。
5. 语义不变：recognition 分层、Root approval、AgentEndpoint MITM 与 launch grant 都不改。

## 顺序

- [ ] typed `SigningIdentifier` / `TeamID`，含字符集与长度校验
- [ ] codesignature 从字段生成固定模板 requirement，取消 caller 提交表达式的入口
- [ ] 删除两处 `strings.Contains` 安全判定
- [ ] repositorycheck 增加结构门禁，禁止 catalog 出现 raw requirement literal
- [ ] 边界测试：真实安装仍识别；错 identifier / 错 Team ID / 未签名 / 被篡改全部拒绝；
      字符串走私不再可表达；Linux 仍无 recognized tier

## 门禁

`gofmt -l .`、`go vet ./...`、`go test -count=1 ./...`、`go test -race`（触及包）、
`go run ./cmd/repositorycheck`、`make check-release-build`、`pnpm --dir ui/desktop run check`、
`git diff --check`、工作树干净。不 push。

## 完成陈述

> 一个写错的 catalog 条目无法再扩大 Root 的交付对象，因为 requirement 不再是 catalog 能写的
> 东西——它由受校验的字段生成。
