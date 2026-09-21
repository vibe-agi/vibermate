// Paste the generated response.js into the Response JavaScript editor.
const state = identity.state();
const edit = identity.operations(state.rows, true);
// Preserve non-AI error bodies (including HTML) and the provider's error status.
if (response.statusCode < 200 || response.statusCode >= 300) return;
// [DONE] normally bypasses the JS runtime. Never rely on receiving it as a flush callback.
if (response.body.trim() === '[DONE]') {
  finish('');
  identity.save(state);
  return;
}
const payload = identity.parse(response.body);
if (!identity.object(payload)) identity.fail();
let streamed = false;

function channel(key, kind) {
  let value = state.streams.find(function (item) { return item.key === key; });
  if (value && value.kind !== kind) identity.fail();
  if (!value) {
    if (state.streams.length >= 32 || key.length > 256) identity.fail();
    value = { key: key, kind: kind, pending: '', sent: 0, complete: false };
    state.streams.push(value);
  }
  return value;
}
function textDelta(key, value) {
  if (typeof value !== 'string') identity.fail();
  const item = channel(key, 'text');
  const input = item.pending + value;
  const pattern = new RegExp(state.rows.map(function (row) { return identity.quote(row.to); }).join('|'), 'gu');
  const pieces = [];
  let end = 0;
  let match;
  while ((match = pattern.exec(input)) !== null) {
    const literal = input.slice(end, match.index);
    if (identity.reserved(literal)) identity.fail();
    pieces.push(literal, state.rows.find(function (row) { return row.to === match[0]; }).from);
    end = match.index + match[0].length;
  }
  const tail = input.slice(end);
  let length = Math.min(tail.length, Math.max.apply(null, state.rows.map(function (row) { return row.to.length - 1; })));
  let candidates = [];
  for (; length > 0; length -= 1) {
    candidates = state.rows.filter(function (row) { return row.to.startsWith(tail.slice(-length)); });
    if (candidates.length) break;
  }
  const literal = tail.slice(0, tail.length - length);
  if (identity.reserved(literal)) identity.fail();
  pieces.push(literal);
  const pending = length ? tail.slice(-length) : '';
  // Emit a prefix immediately only when both literal and every possible restored
  // value share it. Thus a normal trailing "/" or Windows drive is not lost.
  let safe = 0;
  while (safe < pending.length && candidates.every(function (row) { return row.from.charAt(safe) === pending.charAt(safe); })) safe += 1;
  pieces.push(pending.slice(0, safe));
  const output = pieces.join('').slice(item.sent);
  item.pending = pending;
  item.sent = safe;
  streamed = true;
  return output;
}
function jsonDelta(key, value) {
  if (typeof value !== 'string') identity.fail();
  const item = channel(key, 'json');
  if (item.complete) {
    if (value.trim() !== '') identity.fail();
    return value;
  }
  item.pending += value;
  if (item.pending.length > 16384) identity.fail();
  let parsed;
  try { parsed = JSON.parse(item.pending); } catch (_) { streamed = true; return ''; }
  parsed = identity.parse(item.pending);
  if (!identity.object(parsed)) identity.fail();
  const before = JSON.stringify(parsed);
  const restored = JSON.stringify(edit.data(parsed));
  const output = before === restored ? item.pending : restored;
  item.pending = '';
  item.complete = true;
  streamed = true;
  return output;
}
function finish(prefix) {
  state.streams.forEach(function (item) {
    if (!item.key.startsWith(prefix)) return;
    // No safe mechanism exists to append a late delta to a named stop event.
    // Fail explicitly instead of silently discarding buffered text / arguments.
    if (item.kind === 'text' ? item.pending.length !== item.sent : !item.complete) identity.fail();
  });
  state.streams = state.streams.filter(function (item) { return !item.key.startsWith(prefix); });
}
function index(value) {
  if (!Number.isSafeInteger(value) || value < 0) identity.fail();
  return String(value);
}
function responseKey(value) {
  if (value.output_index !== undefined) return 'r:' + index(value.output_index) + ':';
  if (typeof value.item_id !== 'string' || !value.item_id || value.item_id.length > 128) identity.fail();
  return 'r:' + JSON.stringify(value.item_id) + ':';
}
function responsesEvent(value) {
  const type = value.type;
  if (type === 'response.output_text.delta' || type === 'response.refusal.delta') {
    const kind = type === 'response.refusal.delta' ? 'refusal' : 'text';
    value.delta = textDelta(responseKey(value) + index(value.content_index) + ':' + kind, value.delta);
  } else if (type === 'response.function_call_arguments.delta') {
    value.delta = jsonDelta(responseKey(value) + 'args', value.delta);
  } else if (type === 'response.custom_tool_call_input.delta') {
    value.delta = textDelta(responseKey(value) + 'custom', value.delta);
  } else if (type === 'response.output_text.done' || type === 'response.refusal.done') {
    const field = type === 'response.refusal.done' ? 'refusal' : 'text';
    finish(responseKey(value) + index(value.content_index) + ':' + field);
    edit.field(value, field);
  } else if (type === 'response.function_call_arguments.done') {
    finish(responseKey(value) + 'args'); edit.args(value, 'arguments', false);
  } else if (type === 'response.custom_tool_call_input.done') {
    finish(responseKey(value) + 'custom'); edit.field(value, 'input');
  } else if (type === 'response.content_part.added' || type === 'response.content_part.done') {
    if (type.endsWith('.done')) finish(responseKey(value) + index(value.content_index) + ':');
    if (type.endsWith('.added') && identity.object(value.part) && value.part.type === 'output_text' && typeof value.part.text === 'string') {
      value.part.text = textDelta(responseKey(value) + index(value.content_index) + ':text', value.part.text);
    } else edit.block(value.part, type.endsWith('.added'));
  } else if (type === 'response.output_item.added' || type === 'response.output_item.done') {
    if (type.endsWith('.done')) finish(responseKey(value));
    edit.block(value.item, type.endsWith('.added'));
  } else if (identity.object(value.response)) {
    if (type === 'response.completed' || type === 'response.incomplete' || type === 'response.failed') finish('');
    edit.responseBody(value.response, type === 'response.created' || type === 'response.in_progress');
  }
  // Reasoning, encrypted content, signature and provider extension events are opaque.
}
function anthropicEvent(value) {
  if (value.type === 'content_block_start') {
    const block = value.content_block;
    if (block.type === 'text' && typeof block.text === 'string') block.text = textDelta('a:' + index(value.index) + ':text', block.text);
    else edit.block(block, true);
  } else if (value.type === 'content_block_delta') {
    const delta = value.delta;
    if (!identity.object(delta)) identity.fail();
    if (delta.type === 'text_delta') delta.text = textDelta('a:' + index(value.index) + ':text', delta.text);
    else if (delta.type === 'input_json_delta') delta.partial_json = jsonDelta('a:' + index(value.index) + ':args', delta.partial_json);
  } else if (value.type === 'content_block_stop') finish('a:' + index(value.index) + ':');
  else if (value.type === 'message_stop' || value.type === 'error') finish('');
  else if (value.type === 'message_start' && identity.object(value.message)) edit.responseBody(value.message, true);
}
function chatEvent(value) {
  value.choices.forEach(function (choice) {
    const prefix = 'c:' + index(choice.index) + ':';
    const delta = choice.delta;
    if (identity.object(delta)) {
      ['content', 'refusal'].forEach(function (field) {
        if (typeof delta[field] === 'string') delta[field] = textDelta(prefix + field, delta[field]);
      });
      if (Array.isArray(delta.tool_calls)) delta.tool_calls.forEach(function (call) {
        if (identity.object(call.function) && typeof call.function.arguments === 'string') {
          call.function.arguments = jsonDelta(prefix + 'tool:' + index(call.index), call.function.arguments);
        } else if (identity.object(call.custom) && typeof call.custom.input === 'string') {
          call.custom.input = textDelta(prefix + 'custom:' + index(call.index), call.custom.input);
        }
      });
      if (identity.object(delta.function_call) && typeof delta.function_call.arguments === 'string') delta.function_call.arguments = jsonDelta(prefix + 'function', delta.function_call.arguments);
    }
    if (choice.finish_reason !== undefined && choice.finish_reason !== null) finish(prefix);
  });
}

if (!response.streaming) edit.responseBody(payload, false);
else if (typeof payload.type === 'string' && payload.type.startsWith('response.')) responsesEvent(payload);
else if (Array.isArray(payload.choices)) chatEvent(payload);
else anthropicEvent(payload);
identity.save(state);
if (streamed || identity.changed()) response.body = JSON.stringify(payload);
