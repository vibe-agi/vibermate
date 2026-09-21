# JavaScript 脚本目录

用于存放 ViberMate 请求与响应转换的独立 JavaScript 脚本文件，例如“隐藏本机身份”。

## 脚本

- [隐藏本机身份](hide-local-identity/README.md)：用户名、主目录、工作区根路径的请求替换与响应还原。
- [隐藏客户端元信息](hide-client-metadata/README.md)：独立的 version、install id、User-Agent 请求头替换。

各功能目录提供可直接粘贴的脚本，请按各自 README 安装。
文件放入本目录不会自动生效；使用时须配置到前端代码库，保存并绑定到相应流量策略。
“隐藏本机身份”必须将请求／响应脚本配置到同一个条目，不能只安装请求脚本；
“隐藏客户端元信息”仅有请求脚本，响应脚本留空。

此目录没有自动加载功能。当前 UI 内置示例仍定义在
`ui/flutter_app/lib/features/workbench/code_library_view.dart` 中，本次未替换该示例，也未迁移数据库中的已发布策略。

请勿在脚本中硬编码真实密钥、凭据或个人身份数据。
