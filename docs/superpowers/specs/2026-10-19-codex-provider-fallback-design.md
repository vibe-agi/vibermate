# Optional Codex provider for users without a native login

Status: approved direction. The user explicitly chose `--codex-provider`; ordinary launches retain their current behavior. The implementation is additive and does not detect, repair, or replace native login state.

## User experience

Ordinary launch remains `vibermate run --env work -- codex`. A user without a Codex account can opt into `vibermate run --env work --codex-provider -- codex`. A configured Environment supplies the upstream account. No OpenAI login is required for the custom provider. Show one concise localized activation message on stderr, without credentials or implementation details.

The option is a valueless flag before `--`, accepted once. It requires an explicit Environment and a recognized Codex launch recipe. Transparent/original-only captures and other clients cannot use it. An unavailable managed Codex destination produces a useful localized error and finalizes any created Capture. Existing commands and their errors retain current behavior when the flag is absent.

Use the same Codex home and native history. New `vibermate` provider sessions are selectable on subsequent launches with this flag. Old `openai` sessions can be addressed by UUID using `vibermate run --env work --codex-provider -- codex resume <id>`. The native picker filters by provider; `--all` only removes cwd filtering. Document this limitation and the native ownership/fork behavior. Do not migrate or edit stored history/provider metadata. Codex itself may update a resumed thread's active settings.

## Architecture and authority

Reuse the authenticated Capture proxy and its per-run capability. Codex's provider points to an existing managed Codex Client Flow; the proxy consumes its own authentication and the existing provider transport applies the Environment's actual upstream account. No new listener, unauthenticated gateway, fake OAuth login, accounts/check impersonation, or upstream credential export is introduced.

The launcher selects only a canonical authority present in both the validated grant's protected and managed sets: prefer `chatgpt.com:443` with `https://chatgpt.com/backend-api/codex`, otherwise `api.openai.com:443` with `https://api.openai.com/v1`. If neither qualifies, fail. Do not infer permission from a model name, presence of a Root, or protected authority alone. Do not create or expand routing authority. Managed scope already requires every semantic destination for that Client Flow to be upstream-owned.

Inject per-invocation configuration for stable provider ID `vibermate`, display name `ViberMate`, wire API `responses`, `requires_openai_auth=false`, and HTTP transport without WebSockets. Use the existing non-secret `vibermate-local-proxy` placeholder via reserved child environment variable `VIBERMATE_CODEX_PROVIDER_TOKEN` / provider `env_key`. This placeholder is not authentication: real admission remains the scoped proxy capability. Never reuse a proxy/control capability as an upstream bearer. The fixed provider env key avoids inheriting a differently configured provider bearer; command-backed auth conflicts must fail before use.

Do not read or write auth.json, Keychain, config.toml, session files, or the user's persistent environment. Preserve CODEX_HOME. Do not inject model or reasoning choices. Normal Codex resume selection rules still apply; retaining an old session's model can use the client's explicit `--model` option.

The original child invocation remains immutable Capture evidence. Put the new launch policy in a focused runlauncher helper. Reject explicit child overrides that would contradict the provider choice (model_provider, the reserved provider definition, --oss/--local-provider, and conflicting base URL inputs). Keep unrelated model, reasoning, sandbox, profile, prompt and resume arguments intact. Argument detection must respect option values, attached/compact forms and the `--` separator; prompt text is not a flag. Reuse the existing invocation parser where possible, without loosening its fail-closed grammar.

Keep existing per-Capture backend isolation, trust verification, proxy NO_PROXY protection, remote materialization, heartbeat, revocation and cleanup. Unknown executables never gain authority by their basename. All paths after grant creation must finalize on provider preparation failure.

## Compatibility evidence

The research target is official Codex `rust-v0.163.0`, commit `83086188ae0614bd882818ff1aa23f3194962012`. Its custom provider can skip OpenAI onboarding; local auth files are separate from thread storage. Its picker filters on provider ID and a UUID lookup reads the thread directly. Explicit invocation provider settings take precedence during cold resume; active thread ownership remains enforced. Native `openai` cannot simply be replaced in the configured provider table.

Source anchors: [provider authentication](https://github.com/openai/codex/blob/83086188ae0614bd882818ff1aa23f3194962012/codex-rs/model-provider/src/auth.rs#L197-L221), [resume settings](https://github.com/openai/codex/blob/83086188ae0614bd882818ff1aa23f3194962012/codex-rs/tui/src/app/config_persistence.rs#L31-L54), [UUID lookup](https://github.com/openai/codex/blob/83086188ae0614bd882818ff1aa23f3194962012/codex-rs/tui/src/lib.rs#L789-L820).

## Verification

Behavior tests cover CLI parsing, opt-in-only configuration, managed authority selection/rejection, conflicting invocation inputs, Capture cleanup, and unchanged credentials/home. Native acceptance uses fresh synthetic homes and loopback HTTP fixtures only; it must prove an empty login can send a Responses request through the generated provider and can resume saved synthetic history. No real upstream account or user conversation is used. Include actual launch arguments and proxy admission in the acceptance boundary, and verify that upstream-facing requests do not contain proxy credentials. Tests appropriate to touched packages, formatting, Go vet, native-secrets CLI build and repository checks complete the change.

## Out of scope

Automatic fallback after login errors, interception of `codex login`, a new models-refresh command, a cross-provider picker UI, Codex source changes, Cloud/connector OAuth features, changes to Cyber capability selection, releasing/replacing the installed application, and modifying existing user data.
