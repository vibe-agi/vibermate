// 客户端元信息替换 v1：仅请求头；不修改正文、响应、账号或会话标识。
// 下列值仅为示例，不代表上游接受的客户端版本。发布前请按 README 验证并修改。
// 设为 null 可单独关闭一项；空字符串会报错，避免误配置后发送原始值。
(function () {
  "use strict";

  const replacement = {
    version: "0.0.0",
    installId: "00000000-0000-4000-8000-000000000001",
    userAgent: "vibermate-client/0.0.0"
  };

  // 只匹配完整头名。不同客户端不一定使用这些头；未知字段不会被猜测性改写。
  const versionHeaders = ["version", "x-client-version", "x-app-version", "x-codex-version"];
  const installHeaders = [
    "install-id", "installation-id", "x-install-id", "x-installation-id",
    "x-codex-install-id", "x-codex-installation-id"
  ];

  function validValue(value, pattern, maximum) {
    return value === null || (
      typeof value === "string" && value.length > 0 && value.length <= maximum &&
      !/[^\x20-\x7e]/.test(value) && pattern.test(value)
    );
  }

  // 校验全部配置后才修改。错误不回显头内容或真实标识。
  if (!validValue(replacement.version, /^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$/, 64)) {
    throw new Error("client metadata: invalid replacement version");
  }
  if (!validValue(replacement.installId, /^[A-Za-z0-9][A-Za-z0-9._:-]*$/, 128)) {
    throw new Error("client metadata: invalid replacement install id");
  }
  if (!validValue(replacement.userAgent, /^[\x21-\x7e](?:[\x20-\x7e]*[\x21-\x7e])?$/, 512)) {
    throw new Error("client metadata: invalid replacement User-Agent");
  }

  function replaceExisting(names, value) {
    if (value === null) return;
    for (const name of names) {
      if (Object.prototype.hasOwnProperty.call(request.headers, name)) {
        // 收敛成单值，避免重复头中的旧值继续外发。
        request.headers[name] = [value];
      }
    }
  }

  replaceExisting(versionHeaders, replacement.version);
  replaceExisting(installHeaders, replacement.installId);
  if (replacement.userAgent !== null) {
    // 托管转发的协议编码器可能不保留原 User-Agent，因此有意补上这一项。
    request.headers["user-agent"] = [replacement.userAgent];
  }
})();
