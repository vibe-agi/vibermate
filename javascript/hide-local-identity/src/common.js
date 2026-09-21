// ViberMate local identity v1. No imports, I/O, randomness or shared globals.
const identity = (function () {
  const namespace = '__vmi1_';
  const userNamespace = '\u27eavmi1_';
  const contextKey = 'vibermateLocalIdentityV1';
  let changed = false;
  let visits = 0;

  function fail() { throw new Error('Local identity transform cannot safely process this message'); }
  function object(value) { return value !== null && typeof value === 'object' && !Array.isArray(value); }
  function reserved(value) { return value.includes(namespace) || value.includes(userNamespace); }
  function bound(depth) { if (depth > 64 || ++visits > 100000) fail(); }
  function validate(value, depth) {
    bound(depth);
    if (typeof value === 'number' && (!Number.isFinite(value) || Object.is(value, -0) || (Number.isInteger(value) && !Number.isSafeInteger(value)))) fail();
    if (value !== null && typeof value === 'object') {
      Object.keys(value).forEach(function (key) { validate(value[key], depth + 1); });
    }
  }
  function parse(source) {
    let value;
    try { value = JSON.parse(source); } catch (_) { fail(); }
    validate(value, 0);
    // JSON.stringify must not silently round a decimal in an unrelated field.
    const tokens = /"(?:[^"\\]|\\[\s\S])*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g;
    let token;
    while ((token = tokens.exec(source)) !== null) {
      if (token[0].charAt(0) !== '"' && decimal(token[0]) !== decimal(JSON.stringify(Number(token[0])))) fail();
    }
    return value;
  }
  function decimal(value) {
    const parts = /^(-?)(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/.exec(value);
    if (!parts) fail();
    const digits = (parts[2] + (parts[3] || '')).replace(/^0+/, '');
    if (!digits) return '0';
    const trimmed = digits.replace(/0+$/, '');
    return parts[1] + trimmed + 'e' + (Number(parts[4] || 0) - (parts[3] || '').length + digits.length - trimmed.length);
  }
  function quote(value) { return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); }
  function punctuation(value) { return /[\s/\\'"`<>()[\]{},;:=!?。！？；，：、（）【】「」『』]/.test(value); }
  function word(value) {
    // Goja's regexp implementation does not implement all JS Unicode property escapes.
    // Treat non-ASCII, non-punctuation characters conservatively as name characters.
    return /[A-Za-z0-9_.@+$~%-]/.test(value) ||
      (value !== '' && value.charCodeAt(0) >= 128 && !punctuation(value));
  }
  function terminalPeriod(text, offset) {
    return text.charAt(offset) === '.' && (offset + 1 === text.length || punctuation(text.charAt(offset + 1)));
  }
  function pathBoundary(text, start, length) {
    const before = text.charAt(start - 1);
    const after = text.charAt(start + length);
    const prefix = text.slice(0, start);
    // Windows drive paths start after the third slash in file:///C:/...;
    // POSIX paths include that slash in the matched path itself.
    const fileURI = prefix.endsWith('file://') ||
      (prefix.endsWith('file:///') && /^[A-Za-z]:\//.test(text.slice(start)));
    if (!fileURI && (word(before) || before === '/' || before === '\\')) return false;
    return after === '' || punctuation(after) || terminalPeriod(text, start + length) ||
      /^%(?:2f|5c)/i.test(text.slice(start + length));
  }
  function usernameBoundary(text, start, length, privateName, hint) {
    if (word(text.charAt(start - 1)) || (word(text.charAt(start + length)) && !terminalPeriod(text, start + length))) return false;
    // A local account named "root" does not make every mention of a tree root private.
    const ambiguous = privateName.length < 3 || /^(?:root|admin|administrator|user|test|guest|system)$/i.test(privateName);
    return !ambiguous || /^(?:user(?:name)?|local_user|login)$/i.test(hint || '') ||
      /(?:\buser(?:name)?|\blogin|用户名|用户)\s*(?::|=|\bis\b)?\s*["']?$/iu.test(text.slice(Math.max(0, start - 48), start));
  }
  function mappings(runtime) {
    const user = runtime.user || {};
    const workspace = runtime.workspace || {};
    if (!user.name || /[\\/]/.test(user.name) || !user.homeDirectory || !workspace.root) fail();
    const rows = [];
    function add(from, to, kind) {
      if (!from || reserved(from)) fail();
      if (!rows.some(function (row) { return row.from === from; })) rows.push({ from: from, to: to, kind: kind });
    }
    function addPath(value, kind) {
      const windows = /^(?:[A-Za-z]:[\\/]|\\\\)/.test(value);
      const native = value.replace(windows ? /[\\/]+$/ : /\/+$/, '');
      if (!native || !(/^(?:\/|[A-Za-z]:[\\/]|\\\\)/.test(native)) || /^[A-Za-z]:$/.test(native)) fail();
      const paths = [native];
      if (/^(?:[A-Za-z]:\\|\\\\)/.test(native)) paths.push(native.replace(/\\/g, '/'));
      paths.forEach(function (path, index) {
        const root = /^[A-Za-z]:[\\/]/.test(path) ? path.slice(0, 3) : path.startsWith('\\\\') ? '\\\\' : '/';
        const label = kind + (index ? '_slash' : '');
        const alias = root + namespace + label + '__';
        add(path, alias, 'path');
        const uri = encodeURI(path);
        if (uri !== path) add(uri, encodeURI(root + namespace + label + '_uri__'), 'path');
        const encoded = encodeURIComponent(path);
        if (encoded !== path && encoded !== uri) add(encoded, encodeURIComponent(root + namespace + label + '_encoded__'), 'path');
      });
    }
    // Equal roots intentionally have one canonical alias; no ambiguous reverse entry.
    addPath(workspace.root, 'workspace');
    addPath(user.homeDirectory, 'home');
    add(user.name, userNamespace + 'user\u27eb', 'user');
    rows.sort(function (left, right) { return right.from.length - left.from.length; });
    return rows;
  }
  function rewriter(rows, restoring) {
    const entries = rows.slice().sort(function (a, b) { return (restoring ? b.to.length - a.to.length : b.from.length - a.from.length); });
    const pattern = new RegExp(entries.map(function (row) { return quote(restoring ? row.to : row.from); }).join('|'), 'gu');
    return function (value, hint, allowReserved) {
      if (typeof value !== 'string') return value;
      if (!restoring && !allowReserved && reserved(value)) fail();
      const pieces = [];
      let end = 0;
      let match;
      pattern.lastIndex = 0;
      while ((match = pattern.exec(value)) !== null) {
        const offset = match.index;
        const row = entries.find(function (entry) {
          const key = restoring ? entry.to : entry.from;
          return value.startsWith(key, offset) && (restoring || (entry.kind === 'path' ?
            pathBoundary(value, offset, key.length) : usernameBoundary(value, offset, key.length, entry.from, hint)));
        });
        if (!row) {
          // A rejected long match must not swallow a valid home/name inside it.
          pattern.lastIndex = offset + (value.codePointAt(offset) > 65535 ? 2 : 1);
          continue;
        }
        pieces.push(value.slice(end, offset), restoring ? row.from : row.to);
        end = offset + (restoring ? row.to.length : row.from.length);
        pattern.lastIndex = end;
      }
      const result = pieces.join('') + value.slice(end);
      if (restoring && reserved(result)) fail();
      return result;
    };
  }
  function operations(rows, restoring) {
    const rewrite = rewriter(rows, restoring);
    function text(value, hint) {
      const result = rewrite(value, hint);
      if (result !== value) changed = true;
      return result;
    }
    function field(value, key) { if (typeof value[key] === 'string') value[key] = text(value[key], key); }
    function guard(value, depth, allowReserved) {
      bound(depth || 0);
      if (typeof value === 'string') {
        if ((!restoring && !allowReserved && reserved(value)) || rewrite(value, '', true) !== value) fail();
      }
      if (value !== null && typeof value === 'object') Object.keys(value).forEach(function (key) {
        if ((!restoring && !allowReserved && reserved(key)) || rewrite(key, '', true) !== key) fail();
        guard(value[key], (depth || 0) + 1, allowReserved);
      });
    }
    function data(value, hint, depth) {
      bound(depth || 0);
      if (typeof value === 'string') return text(value, hint);
      if (value !== null && typeof value === 'object') {
        Object.keys(value).forEach(function (key) {
          // Application keys, like protocol keys, are not renamed implicitly.
          if ((!restoring && reserved(key)) || rewrite(key, '', true) !== key) fail();
          value[key] = data(value[key], key, (depth || 0) + 1);
        });
      }
      return value;
    }
    function args(value, key, allowEmpty) {
      if (typeof value[key] !== 'string') fail();
      if (value[key] === '' && allowEmpty) return;
      const parsed = parse(value[key]);
      if (!object(parsed)) fail();
      const before = JSON.stringify(parsed);
      const encoded = JSON.stringify(data(parsed));
      // Preserve argument formatting if none of its string values changed.
      if (before !== encoded) { value[key] = encoded; changed = true; }
    }
    function block(value, allowEmpty) {
      if (!object(value)) fail();
      switch (value.type) {
        case 'text': case 'input_text': case 'output_text': case 'summary_text':
          field(value, 'text'); break;
        case 'refusal': field(value, 'refusal'); break;
        case 'message': message(value, allowEmpty); break;
        case 'tool_use': value.input = data(value.input); break;
        case 'tool_result': value.content = content(value.content); break;
        case 'function_call': args(value, 'arguments', allowEmpty); break;
        case 'function_call_output': value.output = typeof value.output === 'string' ? text(value.output) : content(value.output); break;
        case 'custom_tool_call': field(value, 'input'); break;
        case 'custom_tool_call_output': value.output = typeof value.output === 'string' ? text(value.output) : content(value.output); break;
        case 'document':
          if (object(value.source) && value.source.type === 'text') field(value.source, 'data');
          field(value, 'title'); field(value, 'context'); break;
        case 'thinking':
          // Thinking and its signature are an opaque pair, not editable prose.
          if (!restoring) guard(value.thinking, 0, true); break;
        case 'reasoning':
          // Keep provider-specific reasoning/encrypted continuation bytes intact.
          if (!restoring) { guard(value.summary, 0, true); guard(value.content, 0, true); }
          break;
        case 'redacted_thinking': case 'image': case 'input_image': case 'image_url':
        case 'input_audio': case 'audio': case 'file': case 'input_file': break;
        default:
          // Unknown content is not guessed at. Known identities may not leak through it.
          guard(value);
      }
    }
    function content(value) {
      if (typeof value === 'string') return text(value);
      if (value === null || value === undefined) return value;
      if (!Array.isArray(value)) fail();
      value.forEach(function (part) { block(part, false); });
      return value;
    }
    function message(value, allowEmpty) {
      if (!object(value)) fail();
      if ('content' in value) value.content = content(value.content);
      field(value, 'refusal');
      if (Array.isArray(value.tool_calls)) value.tool_calls.forEach(function (call) {
        if (call.type === 'function' && object(call.function)) args(call.function, 'arguments', allowEmpty);
        else if (call.type === 'custom' && object(call.custom)) field(call.custom, 'input');
        else if (!restoring) guard(call);
      });
      if (object(value.function_call)) args(value.function_call, 'arguments', allowEmpty);
      if (!restoring) guard(value.reasoning_content, 0, true);
    }
    function responseBody(value, allowEmpty) {
      if (Array.isArray(value.output)) value.output.forEach(function (item) { block(item, allowEmpty); });
      if (Array.isArray(value.content)) value.content = content(value.content);
      if (Array.isArray(value.choices)) value.choices.forEach(function (choice) { if (object(choice.message)) message(choice.message, allowEmpty); });
      // Do not rewrite IDs, tool names, model, usage, annotations, logprobs or errors.
    }
    return { text: text, field: field, data: data, args: args, block: block, content: content, message: message, responseBody: responseBody, guard: guard };
  }
  function save(state) {
    // Leave room below Runtime's 64 KiB / 1024-value context limits.
    if (JSON.stringify(state).length > 24000) fail();
    context[contextKey] = state;
  }
  function state() {
    const value = context[contextKey];
    if (!object(value) || value.version !== 1 || !Array.isArray(value.rows) || !value.rows.length || !Array.isArray(value.streams)) fail();
    return value;
  }
  return { fail: fail, object: object, parse: parse, validate: validate, quote: quote, reserved: reserved,
    mappings: mappings, operations: operations, save: save, state: state, changed: function () { return changed; } };
})();
