// ViberMate agent IDs v2: request only, leave the response script empty.
// Replaces identifiers Claude Code and Codex generate for themselves and send
// as request metadata with stable stand-ins derived from the key below. No
// imports, I/O, randomness or shared globals.
(function () {
  "use strict";

  // Stand-ins are derived from this text. Before publishing, replace it with
  // 16 to 256 printable ASCII characters of your own: anyone who knows an
  // original ID and this text can recognise its stand-in.
  const key = "replace this with your own key text";

  // Set an item to false to leave that identifier as the agent sent it.
  const hide = {
    // cc_prompt_id in Claude Code's billing line at the start of the system
    // prompt, and the same ID in its X-Claude-Code-Prompt-Id header
    claudePromptId: true,
    // device_id in Claude Code's metadata.user_id, the same for every session
    claudeDeviceId: true,
    // installation_id in Codex's client_metadata and X-Codex-Turn-Metadata
    codexInstallationId: true
  };

  if (typeof key !== "string" || key.length < 16 || key.length > 256 || /[^\x20-\x7e]/.test(key)) {
    throw new Error("agent IDs: the key must be 16 to 256 printable ASCII characters");
  }
  Object.keys(hide).forEach(function (name) {
    if (typeof hide[name] !== "boolean") {
      throw new Error("agent IDs: each hide setting must be true or false");
    }
  });

  const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
  const deviceHex = /^[0-9a-f]{16,64}$/i;

  function object(value) {
    return value !== null && typeof value === "object" && !Array.isArray(value);
  }
  function parse(text) {
    try { return JSON.parse(text); } catch (_) { return null; }
  }
  function headerValues(name) {
    const value = request.headers ? request.headers[name] : undefined;
    if (typeof value === "string") return [value];
    return Array.isArray(value) ? value.filter(function (item) { return typeof item === "string"; }) : [];
  }

  // HMAC-SHA-256 over ASCII text: the key and every identifier are ASCII.
  const rounds = [
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
  ];
  function rotate(value, count) {
    return (value >>> count) | (value << (32 - count));
  }
  function sha256(bytes) {
    const state = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19];
    const bits = bytes.length * 8;
    const data = bytes.slice();
    data.push(0x80);
    while (data.length % 64 !== 56) data.push(0);
    data.push(0, 0, 0, 0, (bits >>> 24) & 255, (bits >>> 16) & 255, (bits >>> 8) & 255, bits & 255);
    const words = new Array(64);
    for (let offset = 0; offset < data.length; offset += 64) {
      for (let index = 0; index < 16; index++) {
        const at = offset + index * 4;
        words[index] = (data[at] << 24) | (data[at + 1] << 16) | (data[at + 2] << 8) | data[at + 3];
      }
      for (let index = 16; index < 64; index++) {
        const low = words[index - 15];
        const high = words[index - 2];
        const s0 = rotate(low, 7) ^ rotate(low, 18) ^ (low >>> 3);
        const s1 = rotate(high, 17) ^ rotate(high, 19) ^ (high >>> 10);
        words[index] = (words[index - 16] + s0 + words[index - 7] + s1) | 0;
      }
      let a = state[0], b = state[1], c = state[2], d = state[3];
      let e = state[4], f = state[5], g = state[6], h = state[7];
      for (let index = 0; index < 64; index++) {
        const t1 = (h + (rotate(e, 6) ^ rotate(e, 11) ^ rotate(e, 25)) + ((e & f) ^ (~e & g)) + rounds[index] + words[index]) | 0;
        const t2 = ((rotate(a, 2) ^ rotate(a, 13) ^ rotate(a, 22)) + ((a & b) ^ (a & c) ^ (b & c))) | 0;
        h = g; g = f; f = e; e = (d + t1) | 0;
        d = c; c = b; b = a; a = (t1 + t2) | 0;
      }
      state[0] = (state[0] + a) | 0; state[1] = (state[1] + b) | 0;
      state[2] = (state[2] + c) | 0; state[3] = (state[3] + d) | 0;
      state[4] = (state[4] + e) | 0; state[5] = (state[5] + f) | 0;
      state[6] = (state[6] + g) | 0; state[7] = (state[7] + h) | 0;
    }
    const digest = [];
    state.forEach(function (word) {
      digest.push((word >>> 24) & 255, (word >>> 16) & 255, (word >>> 8) & 255, word & 255);
    });
    return digest;
  }
  function ascii(text) {
    const bytes = [];
    for (let index = 0; index < text.length; index++) bytes.push(text.charCodeAt(index) & 255);
    return bytes;
  }
  function hex(bytes) {
    return bytes.map(function (byte) { return (byte < 16 ? "0" : "") + byte.toString(16); }).join("");
  }
  // HMAC hashes a key longer than the 64-byte block before padding it.
  let keyBlock = ascii(key);
  if (keyBlock.length > 64) keyBlock = sha256(keyBlock);
  while (keyBlock.length < 64) keyBlock.push(0);
  function mac(text) {
    const inner = keyBlock.map(function (byte) { return byte ^ 0x36; }).concat(ascii(text));
    const outer = keyBlock.map(function (byte) { return byte ^ 0x5c; }).concat(sha256(inner));
    return sha256(outer);
  }

  // The same original always gets the same stand-in, so a session, a device
  // or an installation still looks like one to the provider.
  function standIn(kind, original) {
    const digest = mac(kind + "\n" + original.toLowerCase());
    if (kind === "claude.device") return hex(digest).slice(0, original.length);
    const bytes = digest.slice(0, 16);
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    const text = hex(bytes);
    return text.slice(0, 8) + "-" + text.slice(8, 12) + "-" + text.slice(12, 16) + "-" +
      text.slice(16, 20) + "-" + text.slice(20);
  }

  // Each identifier is replaced where the agent puts it, located by a fixed
  // anchor in the request text. Only those few short fields are parsed and
  // the body is rebuilt once, so a long conversation stays well inside the
  // script time limit. A copy the agent repeats elsewhere is left unchanged.
  const body = typeof request.body === "string" ? request.body : "";
  const spans = [];
  function replaceAt(at, original, kind) {
    for (let index = 0; index < spans.length; index++) {
      if (spans[index][0] === at) return;
    }
    spans.push([at, original.length, standIn(kind, original)]);
  }
  // The end of the JSON string starting at index, or -1.
  function stringEnd(index) {
    for (let at = index; at < body.length; at++) {
      const code = body.charCodeAt(at);
      if (code === 92) at++;
      else if (code === 34) return at;
    }
    return -1;
  }
  // The end of the flat JSON object starting at index, or -1.
  function objectEnd(index) {
    for (let at = index + 1; at < body.length; at++) {
      const code = body.charCodeAt(at);
      if (code === 34) {
        at = stringEnd(at + 1);
        if (at < 0) return -1;
      } else if (code === 125) {
        return at;
      } else if (code === 123) {
        return -1;
      }
    }
    return -1;
  }
  // Every place original occurs between from and to.
  function within(from, to, original, kind) {
    let at = body.indexOf(original, from);
    while (at !== -1 && at + original.length <= to) {
      replaceAt(at, original, kind);
      at = body.indexOf(original, at + original.length);
    }
  }

  if (hide.claudePromptId) {
    const anchor = '"text":"x-anthropic-billing-header:';
    const start = body.indexOf(anchor);
    const end = start < 0 ? -1 : stringEnd(start + 8);
    if (end > 0) {
      const line = body.slice(start + 8, end);
      const field = /(?:^|[\s;])cc_prompt_id=([^;\s]+)/g;
      let match;
      while ((match = field.exec(line)) !== null) {
        if (uuid.test(match[1])) within(start + 8, end, match[1], "claude.prompt");
      }
    }
  }
  if (hide.claudeDeviceId) {
    const anchor = '"metadata":{"user_id":"';
    const start = body.indexOf(anchor);
    const end = start < 0 ? -1 : stringEnd(start + anchor.length);
    const userId = end > 0 ? parse(body.slice(start + anchor.length - 1, end + 1)) : null;
    let device = null;
    if (typeof userId === "string") {
      const parsed = parse(userId);
      if (object(parsed)) {
        device = parsed.device_id;
      } else {
        // Releases before the JSON form wrote user_<device>_account_<uuid>_session_<uuid>.
        const legacy = /^user_([0-9a-f]{16,64})_account_/i.exec(userId);
        device = legacy ? legacy[1] : null;
      }
    }
    if (typeof device === "string" && deviceHex.test(device)) within(start, end, device, "claude.device");
  }
  function installationFrom(text) {
    const parsed = typeof text === "string" ? parse(text) : null;
    const value = object(parsed) ? parsed.installation_id : null;
    return typeof value === "string" && uuid.test(value) ? value : null;
  }
  const installations = [];
  if (hide.codexInstallationId) {
    const anchor = '"client_metadata":{';
    const start = body.indexOf(anchor);
    const end = start < 0 ? -1 : objectEnd(start + anchor.length - 1);
    const fields = end > 0 ? parse(body.slice(start + anchor.length - 1, end + 1)) : null;
    if (object(fields)) {
      [fields["x-codex-installation-id"], installationFrom(fields["x-codex-turn-metadata"])].forEach(function (value) {
        if (typeof value === "string" && uuid.test(value) && installations.indexOf(value) === -1) installations.push(value);
      });
      installations.forEach(function (value) { within(start, end, value, "codex.installation"); });
    }
  }

  if (spans.length > 0) {
    spans.sort(function (left, right) { return left[0] - right[0]; });
    const pieces = [];
    let from = 0;
    spans.forEach(function (span) {
      pieces.push(body.slice(from, span[0]), span[2]);
      from = span[0] + span[1];
    });
    pieces.push(body.slice(from));
    request.body = pieces.join("");
  }

  // Claude Code repeats the prompt ID in its own header; the same stand-in
  // replaces it there, or the header would pair the stand-in with the original.
  if (hide.claudePromptId && request.headers) {
    const value = request.headers["x-claude-code-prompt-id"];
    const values = typeof value === "string" ? [value] : Array.isArray(value) ? value : null;
    if (values !== null) {
      let changed = false;
      const next = values.map(function (item) {
        if (typeof item !== "string" || !uuid.test(item)) return item;
        changed = true;
        return standIn("claude.prompt", item);
      });
      if (changed) request.headers["x-claude-code-prompt-id"] = next;
    }
  }

  // Codex repeats its installation ID in two headers.
  if (hide.codexInstallationId && request.headers) {
    ["x-codex-turn-metadata", "x-codex-installation-id"].forEach(function (name) {
      const value = request.headers[name];
      const values = typeof value === "string" ? [value] : Array.isArray(value) ? value : null;
      if (values === null) return;
      let changed = false;
      const next = values.map(function (item) {
        if (typeof item !== "string") return item;
        const ids = name === "x-codex-installation-id" ? (uuid.test(item) ? [item] : []) : [installationFrom(item)];
        let result = item;
        ids.concat(installations).forEach(function (id) {
          if (typeof id === "string" && result.indexOf(id) !== -1) {
            result = result.split(id).join(standIn("codex.installation", id));
          }
        });
        if (result !== item) changed = true;
        return result;
      });
      if (changed) request.headers[name] = next;
    });
  }
})();
