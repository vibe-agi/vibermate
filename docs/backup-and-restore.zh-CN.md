[English](backup-and-restore.md) · [简体中文](backup-and-restore.zh-CN.md)

# 备份与恢复

ViberMate 的备份是一个新的本地目录，附带经过校验的清单。它不是正在使用的
`runtime.db` 的直接拷贝，不会上传到云端，也不会导出凭据。

## 包含哪些内容

- 一致的 SQLite Runtime 数据库，包括已保留的证据；
- 保存在数据目录中的 Runtime 配置；
- 本机 Proxy CA，包括它的私钥；
- `backup-manifest.json`，记录 Runtime schema 标识，以及每个文件的 SHA-256、大小和
  相对路径。

数据库和 Proxy CA 属于敏感数据，备份格式不会对它们加密。Runtime 目录中的服务器访问/
恢复配置也会一并保留。请像保护原始 Runtime 数据目录一样保护备份目录。
清单用于发现意外改动并检查内部一致性；它不是数字签名，如果攻击者能同时替换文件和
清单，它无法证明备份的来源。

## 不包含哪些内容

- 服务商 API 密钥、OAuth 令牌，以及 Server 的 `server-secrets` 存储；
- macOS 钥匙串中的条目；
- 通过 `--tls-cert` 和 `--tls-key` 传入的证书/私钥文件，以及由 Caddy 等外部 TLS
  终止服务管理的文件；
- 所选 Runtime 数据目录之外的存储。

在同一台 Mac 上，恢复后的数据库引用仍可使用该 Mac 钥匙串中依然存在的凭据。换到另一台
机器后，需要重新连接服务商账号。恢复 Server 时，也必须重新配置凭据。无论哪种情况，
外部的 Server HTTPS 文件都需要单独提供。

## macOS App

打开「设置 → 安全与数据 → 证据存储」，选择「创建备份」或「恢复备份」。ViberMate 要求
先停止所有 Capture，会暂停新的本地出站工作，重启本地 Runtime，并在确认前显示准确的
来源目录和新的目标目录。

恢复总是创建一个新的 Runtime 目录，绝不覆盖当前目录。只有清单、每个文件的哈希、
SQLite 完整性、外键和 schema 兼容性全部通过后，App 才会切换到恢复出的目录。如果恢复
出的 Runtime 无法启动，原有的存储选择就是回滚的依据。

## 原生或容器 Server

先停止所有使用该数据目录的 Server 进程。使用本地持久化存储上的绝对路径：

```sh
vibermated backup-data \
  --source='/srv/vibermate' \
  --target='/srv/backups/vibermate-2026-09-26'

vibermated verify-backup \
  --source='/srv/backups/vibermate-2026-09-26'

vibermated restore-data \
  --source='/srv/backups/vibermate-2026-09-26' \
  --target='/srv/vibermate-restored'
```

容器部署时，可以对挂载出来的宿主机数据卷运行这些命令，也可以用一个同时挂载了两个
目录的一次性容器来运行。不要把正在使用的 SQLite 目录放在网络共享或云盘同步文件夹中。

只有 `verify-backup` 成功后，才用恢复出的目录启动 Server。文件缺失或被改动、出现
多余文件、数据库损坏、外键失效或 schema 不兼容时，命令都会直接失败，不会继续。恢复
失败时，现有的 Runtime 目录保持不变，也绝不会替换一个非空或未知的目标目录。新创建但
未完成的目标目录可能会留下来，供检查或手动删除，但 ViberMate 绝不会使用它。

容器部署的数据卷与回滚说明见 [Docker 部署](docker.zh-CN.md)。
