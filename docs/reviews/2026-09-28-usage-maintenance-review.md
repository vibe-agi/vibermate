# 用量到期清理的写锁与维护预算

范围：VIBERMATE-42/45 规模验收。当前 v1 基线补齐内容外键索引；没有清理真实数据或安装 App。

## 复现

`TestUsageMillionObservationPricedProjection` 的 1,000,001 条用量全部到期后，旧 `CleanupExpired` 一笔事务清理约 10.34 秒；Runtime 后台每次维护只有 5 秒。取消会回滚整笔事务，下次重新处理相同积压；同时占着唯一写连接。

独立失败测试进一步证明：`storageHealthMonitor` 把清理的 `context.DeadlineExceeded` 和关闭时的 `context.Canceled` 也送给存储健康观察者，暂时把 Runtime 标成 unavailable。这是维护预算耗尽，不是数据库不可读或核心审计已丢失的证据。

## 修复边界

- 明确区分后台维护与用户手动清理。后台 `MaintainExpired` 复用同一事务实现，每个平面最多删除 1,000 个已到期标识；批次提交后释放写连接，再继续，取消只回滚当前批次。
- 用户手动 `CleanupExpired` 保留原子完成及准确 receipt，不把部分后台进度冒充一次完整用户操作。
- 共用既有到期索引与内容可达性回收，不加队列、线程池、缓存或第二份保留策略。真实数据库错误仍进入健康观察；只有维护预算／取消不再被当作数据库损坏或代理不可用。
- 本次限制的是每个平面删除的父记录数。内容寻址 GC 仍沿用全局可达性查询，字节量和扫描量不是常数；没有宣称百万条大正文／Raw 的单批耗时已经验证。数据面既有正文／Raw 保留调用也未在本次小修复中重构。

## 检查

```sh
go test ./internal/runtimepersistence ./internal/productruntime ./internal/exchangecontent ./internal/rawevidence -count=1
go test -race -short ./internal/runtimepersistence ./internal/productruntime -run 'TestExpiredMaintenance|TestRetentionBudget|TestStorageHealthMonitor' -count=1
env VIBERMATE_USAGE_SCALE=1 go test ./internal/runtimepersistence -run TestUsageMillionObservationPricedProjection -count=1 -v
```

全部通过。检查覆盖：三种平面各自的批量上限、批次中途失败整体回滚、取消后已提交进度保留、续清、未到期记录与共享内容不受影响；预算／取消不宣告存储不可用、真实数据库失败仍上报。

百万条实测首个 1,000 条批次约 36.8 毫秒；用 250 毫秒预算反复中断并续清，47 次预算约 11.48 秒完成全部记录。总吞吐没有虚报变快，收益是释放写连接和可持续推进。数据来自隔离夹具，不是生产延迟承诺或真实长稳流量的验收。

修复后全 Go 回归、vet、结构检查与定向 race 通过；新候选 `dist/candidates/local-hMbKcEC4/ViberMate.app` 已构建及包校验，默认页签修复亦包含其中。原安装与 `dist/ViberMate.app` 的 daemon SHA-256 均仍为 `b2d337e74aec550aa5e3b5acdbb0ff1eb398a06b18fea8be5c67285ad7e25eb2`。尚未安装／发布，新包的 Web 与最终切换门禁继续。

## 最新包内回归

使用新候选实际 daemon／Web，在 `/private/tmp/vibermate-package-smoke.5JxilL/data-verified` 新建隔离 Server，188 次真实 loopback CONNECT/TLS 请求通过：125／130／131 的运行与会话汇总保持正确，无正文会话目录明确返回 `contentAvailable: false`；59 个模型以同一快照分页为 50＋9，无重复／遗漏；末尾 Runtime 仍 ready，包内 Web HTTP 200。

测试使用合成凭据及本地上游，结束自动停止所有夹具进程。前两次脚本误用了 `dimension` 参数、以及从明细页读取被明确省略的总量，均是验收脚本错误；按当前 `groupBy`／独立总量契约修正脚本后通过，没有放宽产品契约或改测试期望。此处是包内 HTTP 检查，不冒充重新操作 Chrome；新默认页签仍待实际 UI 复验。

包内 daemon SHA-256：`79b19d1d6bb7e386877356e38361639c9141c62a49cf14629b6bd1301488d6de`；CLI：`9649da21ad6ff934c26ae11cf5e8c93db1de1c696ad1a6cba3279d8c8dbd6fe9`。候选仍为未提交源码、本地 ad-hoc 验收包，不可直接指向尚未转换的现用数据库。

## 正文／Raw 清理与外键索引

补充真实仓库写入的 10,001 条独立正文及 Raw 夹具（10,000 条到期、一条未到期）后，首批再次触及真实 5 秒维护预算，错误为 `purge unreferenced transcript nodes: context deadline exceeded`，整批没有提交。问题并非批次大小：SQLite 删除内容父记录时，对六个缺少索引的外键引用反复全表扫描。

当前单一 v1 基线增加六个引用索引：transcript→message，以及 exchange content 的 request／expected／base transcript、system／response message。没有运行时迁移、旧结构兼容或新增 GC 架构。`TestContentDeletionUsesIndexedForeignKeyProbes` 直接检查三个内容父表的删除查询计划，防止外键探测退化为 `SCAN`。

相同夹具补齐索引后，首批 1,000 条正文＋1,000 条 Raw 在 **180.29 毫秒**提交，全部到期内容在 **1.059 秒**完成，未到期正文及 Raw 仍可读取。测试包含实际内容回收和共享 system 引用，不只是空元数据；规模仍为一万条，不能外推百万大正文性能。夹具准备也计入测试总时长 74.15 秒，不与清理耗时混淆。

```sh
go test ./internal/runtimepersistence -run 'TestContentDeletionUsesIndexedForeignKeyProbes|TestExpiredMaintenance|TestStorageStatisticsPreview|TestExpiredExchanges' -count=1
env VIBERMATE_USAGE_SCALE=1 go test ./internal/runtimepersistence -run TestBodyHeavyExpiryFitsMaintenanceBudget -count=1 -v
go test ./tool/convert-v1 -count=1
```

以上检查通过。索引改变当前基线指纹，之前的候选包和静态转换目标不再代表最新结构，需重建候选并重新转换；原目录保持不动。
