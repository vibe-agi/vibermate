// Paste the generated request.js into the Request JavaScript editor.
const payload = identity.parse(request.body);
if (!identity.object(payload)) identity.fail();
const state = { version: 1, rows: identity.mappings(runtime), streams: [] };
const edit = identity.operations(state.rows, false);
let recognized = false;

if ('instructions' in payload) edit.field(payload, 'instructions');
if ('system' in payload) payload.system = edit.content(payload.system);
if (Array.isArray(payload.messages)) {
  recognized = true;
  payload.messages.forEach(function (message) { edit.message(message, false); });
}
if ('input' in payload) {
  recognized = true;
  if (typeof payload.input === 'string') payload.input = edit.text(payload.input);
  else if (Array.isArray(payload.input)) payload.input.forEach(function (item) {
    if (identity.object(item) && !item.type && typeof item.role === 'string') edit.message(item, false);
    else edit.block(item, false);
  });
  else identity.fail();
}
if (!recognized) identity.fail();

if (Array.isArray(payload.tools)) payload.tools.forEach(function (tool) {
  if (!identity.object(tool)) identity.fail();
  edit.field(tool, 'description');
  if (identity.object(tool.function)) {
    edit.field(tool.function, 'description');
    edit.guard(tool.function.parameters);
  }
  // Schemas / enums / constants are constraints, not arbitrary text fields.
  edit.guard(tool.parameters);
  edit.guard(tool.input_schema);
});
if (Array.isArray(payload.functions)) payload.functions.forEach(function (tool) {
  edit.field(tool, 'description'); edit.guard(tool.parameters);
});

identity.save(state); // Build all mappings even when this turn only says "continue".
if (identity.changed()) request.body = JSON.stringify(payload);
