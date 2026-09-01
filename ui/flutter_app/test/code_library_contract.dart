typedef TransformSourceContract = ({String request, String response});

const localIdentityContract = (
  request: r'''const candidates = [
  [runtime.workspace.root, "/workspace/project"],
  [runtime.user.homeDirectory, "/Users/guest"],
  [runtime.user.name, "vibermate-user"],
];
context.redactions = [];
for (let index = 0; index < candidates.length; index += 1) {
  const privateValue = candidates[index][0];
  const publicValue = candidates[index][1];
  if (!privateValue || privateValue === publicValue) continue;
  const encodedPrivate = JSON.stringify(privateValue).slice(1, -1);
  const encodedPublic = JSON.stringify(publicValue).slice(1, -1);
  if (!request.body.includes(encodedPrivate)) continue;
  request.body = request.body.split(encodedPrivate).join(encodedPublic);
  context.redactions.push([encodedPrivate, encodedPublic]);
}''',
  response: restoreRedactionsResponse,
);

const blockSecretsContract = (
  request: r'''const privateKey = /-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----/;
const commonKey = /\b(?:sk-ant-[A-Za-z0-9_-]{16,}|sk-proj-[A-Za-z0-9_-]{16,}|sk-[A-Za-z0-9_-]{20,}|github_pat_[A-Za-z0-9_]{16,}|gh[pousr]_[A-Za-z0-9]{16,}|xox[baprs]-[A-Za-z0-9-]{16,}|AKIA[A-Z0-9]{16})\b/;
if (privateKey.test(request.body) || commonKey.test(request.body)) {
  throw new Error("Request blocked because it contains a private key or access token");
}''',
  response: '',
);

const privateContactsContract = (
  request: r'''context.redactions = [];
function hide(value, kind) {
  const existing = context.redactions.find(function (item) { return item[0] === value; });
  if (existing) return existing[1];
  const suffix = context.redactions.length + 1;
  const visible = kind === "email"
    ? "redacted-email-" + suffix + "@example.invalid"
    : "192.0.2." + suffix;
  context.redactions.push([value, visible]);
  return visible;
}
request.body = request.body.replace(
  /[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/gi,
  function (value) { return hide(value, "email"); }
);
request.body = request.body.replace(/\b(?:\d{1,3}\.){3}\d{1,3}\b/g, function (value) {
  const parts = value.split(".").map(Number);
  const privateAddress = parts.every(function (part) { return part >= 0 && part <= 255; }) &&
    (parts[0] === 10 || parts[0] === 127 ||
      (parts[0] === 172 && parts[1] >= 16 && parts[1] <= 31) ||
      (parts[0] === 192 && parts[1] === 168));
  return privateAddress ? hide(value, "ip") : value;
});''',
  response: restoreRedactionsResponse,
);

const restoreRedactionsResponse = r'''if (Array.isArray(context.redactions)) {
  for (let index = 0; index < context.redactions.length; index += 1) {
    response.body = response.body
      .split(context.redactions[index][1])
      .join(context.redactions[index][0]);
  }
}''';

const transformSourceContracts = <String, Map<String, TransformSourceContract>>{
  'localIdentity': {
    'anthropic_messages': localIdentityContract,
    'openai_responses': localIdentityContract,
    'openai_chat': localIdentityContract,
  },
  'blockSecrets': {
    'anthropic_messages': blockSecretsContract,
    'openai_responses': blockSecretsContract,
    'openai_chat': blockSecretsContract,
  },
  'privateContacts': {
    'anthropic_messages': privateContactsContract,
    'openai_responses': privateContactsContract,
    'openai_chat': privateContactsContract,
  },
  'turnTime': {
    'anthropic_messages': (
      request: '',
      response: r'''const payload = JSON.parse(response.body);
if (response.streaming && !context.turnTimeShown && payload.type === "content_block_delta" && payload.delta && payload.delta.type === "text_delta") {
  const label = runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "");
  payload.delta.text = runtime.annotations.create("turn-time", label) + "\n" + payload.delta.text;
  context.turnTimeShown = true;
} else if (!response.streaming && Array.isArray(payload.content)) {
  payload.content.unshift({
    type: "text",
    text: runtime.annotations.create("turn-time", runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "")),
  });
}
response.body = JSON.stringify(payload);''',
    ),
    'openai_responses': (
      request: '',
      response: r'''const payload = JSON.parse(response.body);
if (response.streaming && !context.turnTimeShown && payload.type === "response.output_text.delta" && typeof payload.delta === "string") {
  const label = runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "");
  payload.delta = runtime.annotations.create("turn-time", label) + "\n" + payload.delta;
  context.turnTimeShown = true;
} else if (!response.streaming && Array.isArray(payload.output)) {
  for (let outputIndex = 0; outputIndex < payload.output.length; outputIndex += 1) {
    const item = payload.output[outputIndex];
    if (!Array.isArray(item.content)) continue;
    for (let contentIndex = 0; contentIndex < item.content.length; contentIndex += 1) {
      const part = item.content[contentIndex];
      if (part.type === "output_text" && typeof part.text === "string") {
        const label = runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "");
        part.text = runtime.annotations.create("turn-time", label) + "\n" + part.text;
        outputIndex = payload.output.length;
        break;
      }
    }
  }
}
response.body = JSON.stringify(payload);''',
    ),
    'openai_chat': (
      request: '',
      response: r'''const payload = JSON.parse(response.body);
const choice = Array.isArray(payload.choices) ? payload.choices[0] : undefined;
if (response.streaming && !context.turnTimeShown && choice && choice.delta && typeof choice.delta.content === "string") {
  const label = runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "");
  choice.delta.content = runtime.annotations.create("turn-time", label) + "\n" + choice.delta.content;
  context.turnTimeShown = true;
} else if (!response.streaming && choice && choice.message && typeof choice.message.content === "string") {
  const label = runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "");
  choice.message.content = runtime.annotations.create("turn-time", label) + "\n" + choice.message.content;
}
response.body = JSON.stringify(payload);''',
    ),
  },
  'replyLanguage': {
    'anthropic_messages': (
      request: r'''const payload = JSON.parse(request.body);
const guidance = "Reply in Simplified Chinese unless the user explicitly requests another language.";
if (typeof payload.system === "string") {
  payload.system += "\n\n" + guidance;
} else if (Array.isArray(payload.system)) {
  payload.system.push({type: "text", text: guidance});
} else {
  payload.system = guidance;
}
request.body = JSON.stringify(payload);''',
      response: '',
    ),
    'openai_responses': (
      request: r'''const payload = JSON.parse(request.body);
const guidance = "Reply in Simplified Chinese unless the user explicitly requests another language.";
payload.instructions = typeof payload.instructions === "string"
  ? payload.instructions + "\n\n" + guidance
  : guidance;
request.body = JSON.stringify(payload);''',
      response: '',
    ),
    'openai_chat': (
      request: r'''const payload = JSON.parse(request.body);
const guidance = "Reply in Simplified Chinese unless the user explicitly requests another language.";
let message;
if (Array.isArray(payload.messages)) {
  for (let index = 0; index < payload.messages.length; index += 1) {
    const candidate = payload.messages[index];
    if (candidate.role === "developer" || candidate.role === "system") {
      message = candidate;
      break;
    }
  }
}
if (message && typeof message.content === "string") {
  message.content += "\n\n" + guidance;
} else {
  if (!Array.isArray(payload.messages)) payload.messages = [];
  payload.messages.unshift({role: "developer", content: guidance});
}
request.body = JSON.stringify(payload);''',
      response: '',
    ),
  },
  'workspaceRules': {
    'anthropic_messages': (
      request: r'''const payload = JSON.parse(request.body);
const rules = {
  "example": "Treat workspace details as confidential and do not repeat secrets.",
  "work": "Treat workspace details as confidential and do not repeat secrets.",
  "personal": "Prefer concise answers and explain destructive steps before running them.",
};
const guidance = rules[runtime.workspace.label];
if (guidance) {
  if (typeof payload.system === "string") {
    payload.system += "\n\n" + guidance;
  } else if (Array.isArray(payload.system)) {
    payload.system.push({type: "text", text: guidance});
  } else {
    payload.system = guidance;
  }
  request.body = JSON.stringify(payload);
}''',
      response: '',
    ),
    'openai_responses': (
      request: r'''const payload = JSON.parse(request.body);
const rules = {
  "example": "Treat workspace details as confidential and do not repeat secrets.",
  "work": "Treat workspace details as confidential and do not repeat secrets.",
  "personal": "Prefer concise answers and explain destructive steps before running them.",
};
const guidance = rules[runtime.workspace.label];
if (guidance) {
  payload.instructions = typeof payload.instructions === "string"
    ? payload.instructions + "\n\n" + guidance
    : guidance;
  request.body = JSON.stringify(payload);
}''',
      response: '',
    ),
    'openai_chat': (
      request: r'''const payload = JSON.parse(request.body);
const rules = {
  "example": "Treat workspace details as confidential and do not repeat secrets.",
  "work": "Treat workspace details as confidential and do not repeat secrets.",
  "personal": "Prefer concise answers and explain destructive steps before running them.",
};
const guidance = rules[runtime.workspace.label];
if (guidance) {
  let message;
  if (Array.isArray(payload.messages)) {
    for (let index = 0; index < payload.messages.length; index += 1) {
      const candidate = payload.messages[index];
      if (candidate.role === "developer" || candidate.role === "system") {
        message = candidate;
        break;
      }
    }
  }
  if (message && typeof message.content === "string") {
    message.content += "\n\n" + guidance;
  } else {
    if (!Array.isArray(payload.messages)) payload.messages = [];
    payload.messages.unshift({role: "developer", content: guidance});
  }
  request.body = JSON.stringify(payload);
}''',
      response: '',
    ),
  },
  'responseModel': {
    'anthropic_messages': (
      request: '',
      response: r'''const payload = JSON.parse(response.body);
if (payload.type === "message_start" && payload.message && typeof payload.message.model === "string") {
  context.responseModel = payload.message.model;
}
if (!response.streaming && typeof payload.model === "string") context.responseModel = payload.model;
if (response.streaming && !context.responseModelShown && context.responseModel && payload.type === "content_block_delta" && payload.delta && payload.delta.type === "text_delta") {
  payload.delta.text = runtime.annotations.create("response-model", context.responseModel) + "\n" + payload.delta.text;
  context.responseModelShown = true;
} else if (!response.streaming && context.responseModel && Array.isArray(payload.content)) {
  payload.content.unshift({
    type: "text",
    text: runtime.annotations.create("response-model", context.responseModel),
  });
}
response.body = JSON.stringify(payload);''',
    ),
    'openai_responses': (
      request: '',
      response: r'''const payload = JSON.parse(response.body);
if (typeof payload.model === "string") context.responseModel = payload.model;
if (payload.response && typeof payload.response.model === "string") {
  context.responseModel = payload.response.model;
}
if (response.streaming && !context.responseModelShown && context.responseModel && payload.type === "response.output_text.delta" && typeof payload.delta === "string") {
  payload.delta = runtime.annotations.create("response-model", context.responseModel) + "\n" + payload.delta;
  context.responseModelShown = true;
} else if (!response.streaming && context.responseModel && Array.isArray(payload.output)) {
  for (let outputIndex = 0; outputIndex < payload.output.length; outputIndex += 1) {
    const item = payload.output[outputIndex];
    if (!Array.isArray(item.content)) continue;
    for (let contentIndex = 0; contentIndex < item.content.length; contentIndex += 1) {
      const part = item.content[contentIndex];
      if (part.type === "output_text" && typeof part.text === "string") {
        part.text = runtime.annotations.create("response-model", context.responseModel) + "\n" + part.text;
        outputIndex = payload.output.length;
        break;
      }
    }
  }
}
response.body = JSON.stringify(payload);''',
    ),
    'openai_chat': (
      request: '',
      response: r'''const payload = JSON.parse(response.body);
if (typeof payload.model === "string") context.responseModel = payload.model;
const choice = Array.isArray(payload.choices) ? payload.choices[0] : undefined;
if (response.streaming && !context.responseModelShown && context.responseModel && choice && choice.delta && typeof choice.delta.content === "string") {
  choice.delta.content = runtime.annotations.create("response-model", context.responseModel) + "\n" + choice.delta.content;
  context.responseModelShown = true;
} else if (!response.streaming && context.responseModel && choice && choice.message && typeof choice.message.content === "string") {
  choice.message.content = runtime.annotations.create("response-model", context.responseModel) + "\n" + choice.message.content;
}
response.body = JSON.stringify(payload);''',
    ),
  },
};

const accountSelectorSourceContracts = <String, String>{
  'loginUser': r'''const accountByLogin = {
  "alice": "account.team-a",
  "bob": "account.team-b",
};
if (!runtime.login.username) {
  throw new Error("ViberMate login is required");
}
const accountId = accountByLogin[runtime.login.username];
if (!accounts.some(function (account) { return account.id === accountId; })) {
  throw new Error("No Account is configured for this ViberMate login");
}
selection.accountId = accountId;''',
};
