# Managed client authentication implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Let users explicitly request Environment-managed model-service authentication through a generic run option, with Codex and Claude adapters.

**Architecture:** Add a generic `--managed-auth` launcher option with client-specific preparation helpers. Reuse authenticated Capture admission and provider-account routing; preserve the native path by default and preserve local history storage.

**Tech Stack:** Go 1.26, existing CLI/runlauncher, JSON locale catalogs, official Codex 0.163.0 acceptance fixtures.

**Spec:** `docs/superpowers/specs/2026-10-19-codex-provider-fallback-design.md`

## Global Constraints

- Explicit `--managed-auth` only; no automatic auth detection or fallback.
- Require explicit `--env`, established supported client ID plus compatible recipe, and existing protected + managed target authority.
- Codex uses its custom Responses provider; Claude uses the existing managed ANTHROPIC_AUTH_TOKEN path. Unknown clients fail; default behavior stays unchanged.
- Prefer `https://chatgpt.com/backend-api/codex`, otherwise `https://api.openai.com/v1`; never expand the grant.
- Provider ID `vibermate`, name `ViberMate`, wire API `responses`, `requires_openai_auth=false`, HTTP transport.
- Child `VIBERMATE_CODEX_PROVIDER_TOKEN=vibermate-local-proxy` is a non-secret placeholder; real authentication is the per-Capture proxy capability.
- Never export upstream OAuth credentials or reuse proxy/control capabilities as upstream bearer tokens.
- Preserve CODEX_HOME, persistent config, auth stores and history; do not set model/reasoning defaults.
- Preserve original child invocation as Capture evidence and existing backend isolation/cleanup.
- No real account/network calls in tests; only synthetic homes and loopback fixtures.
- Native picker remains provider-filtered; document UUID resume and ownership limitations accurately.

## Task 1: Implement the opt-in launch path and user documentation

**Files:**
- Modify `cmd/vibermate/main.go`, `cmd/vibermate/main_test.go`.
- Create focused `internal/runlauncher/managed_auth.go` and `internal/runlauncher/codex_provider.go`, with behavioral tests adjacent to them or in `codex_provider_integration_test.go`.
- Modify `internal/runlauncher/launcher.go`, and existing invocation/environment helpers only where integration needs them.
- Modify/add launcher integration tests beside `internal/runlauncher/launcher_integration_test.go`.
- Modify `locales/en-US.json`, `locales/zh-CN.json` and existing CLI documentation such as README.md.

**Interfaces:**
- Extend `runlauncher.LaunchRequest` with `ManagedAuth bool`, and the CLI's runConfig with `managedAuth bool`.
- Consume the existing validated `capturecontrol.LaunchGrant`; do not add control-plane wire fields.
- A focused generic helper dispatches by established client ID + recipe, prepares child-only arguments/environment or returns a typed user-facing error. Codex helper remains internal; Claude reuses existing managed target/credential logic. Implementation may choose unexported signatures to minimize mutable state.

- [ ] Write behavioral RED tests through parseRun and Launcher.Run. Illustrative independent fixtures:

```go
got, err := parseRun([]string{"run", "--env", "work", "--managed-auth", "--", "codex", "resume", "fixture-id"})
// Expect no error, explicit environment, true managedAuth, unchanged child argv.
// Reject duplicate flag, missing --env, value syntax, and missing child.
```

```go
// A grant with chatgpt.com only protected must reject supplemental auth.
// With chatgpt.com protected + managed, observe provider="vibermate",
// requires_openai_auth=false, chatgpt base URL and child placeholder.
// With only api.openai.com managed, observe the /v1 base.
// With both managed, observe deterministic ChatGPT selection.
// Without opt-in, observe the existing argv/env and no provider override.
```

- [ ] Run focused tests and record the expected behavior failures before production edits.
- [ ] Implement parser and request propagation; retain the existing delimiter and option validation contract. Add new error keys and useful localized diagnostics. Enforce missing environment before Capture creation and validated-client/managed-authority constraints before child execution. Validate adapter/signer ID, not recipe alone; the RED case where another client claims a Codex recipe must remain covered.
- [ ] Cover Claude: opted-in correct managed target emits the existing gateway token and strips conflicting ambient credentials; original-only/protected-only/unsupported identity or recipe rejects and finalizes; normal Claude without the flag stays unchanged. Reuse clientTargetAuthority/usesManagedClientCredential and verify both protected and managed authority rather than introducing a different target resolver.
- [ ] Implement Codex provider preparation. Start with the normal launch env/transport args, add the opt-in provider configuration and reserved child placeholder, preserve original command suffix for isolation validation, and reject conflicting child overrides with argument-aware parsing. Cover attached `-c...`, `--config=...`, provider table overrides, `--oss`/`--local-provider`, base inputs, prompt text and `--` boundaries. Keep profile/model/reasoning arguments that do not contradict the selected provider.
- [ ] Integrate lifecycle cleanup and localized activation notice. Verify rejected launches finalize the created Capture and never start the child. Do not change the normal launch's original login handling.
- [ ] Document exact generic commands for Codex and Claude, normal launch, no-login opt-in, same-provider resume and UUID cross-provider resume. Explain that provider is an internal Codex adapter detail. State the native picker limitation, explicit --model option for a desired resumed model, and native R/F ownership behavior. Avoid claiming Cloud/connector access or full seamless history migration.
- [ ] Run `go test ./cmd/vibermate ./internal/runlauncher ./locales` plus focused affected tests; gofmt, self-review and commit. Write RED/GREEN evidence and exact covering commands to the report file.

## Task 2: Prove empty-login startup and synthetic resume with native Codex

**Files:**
- Create `internal/runlauncher/codex_provider_native_test.go` (or focused test files adjacent to existing native launcher tests).
- Adjust only implementation files needed to correct concrete failures discovered by native acceptance; retain all global constraints.
- Update this plan with validation results after review.

**Interfaces:**
- Consume `runlauncher.LaunchRequest{EnvironmentID: ..., ManagedAuth: true, Command: ...}` from Task 1 and existing controlFixture/native fixture helpers.
- Use `VIBERMATE_CODEX_ACCEPTANCE` as the opt-in absolute native executable path, following existing test conventions. Empty env means skip, not download or use the user's home.

- [ ] Add real subprocess acceptance with synthetic CODEX_HOME, `cli_auth_credentials_store="file"`, disabled unrelated startup integrations, and a loopback fixture that cannot dial upstream. The fixture consumes valid proxy auth and inspects the native Responses request; generate a minimal well-formed response to let Codex finish a turn. Require a fresh no-auth home to reach inference and prove no auth.json/config rewrite is performed by the launcher.
- [ ] Add synthetic history recovery: create a local fixture conversation under an OpenAI provider with only synthetic credentials and loopback transport, then remove only that fixture's auth and resume its UUID through the supplemental provider. Require the next request to include a marker from the prior conversation and use the supplemental destination. Also resume a new supplemental session on a subsequent launch; preserve original thread ID semantics and history path.
- [ ] Add a two-launch assertion that independent grants keep proxy capabilities and Environment routing distinct. Verify no proxy/control/upstream OAuth secret is used as the native Responses bearer; only the non-secret placeholder appears at the client-facing model boundary.
- [ ] Run focused native tests against installed official Codex 0.163.0 with a fresh temp home. Record failure before any necessary fixes, then passing results. Never invoke a real user session or send inference to a real upstream.
- [ ] Run affected Go package tests, gofmt and self-review; commit and report evidence. Root will perform final Go vet, native-secrets builds and repository checks after the task review.
