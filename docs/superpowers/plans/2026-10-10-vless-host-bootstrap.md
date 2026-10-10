# Managed Kernel Host Bootstrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development and test-driven-development. This implements the already approved parent Task4 bootstrap seam while package assembly is independently reviewed.

**Goal:** Let packaged Desktop and standalone Server automatically use the fixed sibling VLESS helper, and start Desktop's authenticated loopback SOCKS listener on its next normal launch.

**Architecture:** Keep executable discovery at the Go daemon entry point shared by both host modes; Dart selects only Desktop's already supported loopback SOCKS bind. No extra Runtime downloader, profile-supplied path, independent SOCKS implementation or process launcher is added. Existing Runtime owns lazy kernel startup and lifecycle.

**Tech Stack:** Existing Go daemon/Host options, Dart Desktop bootstrap, focused Go and Flutter tests.

**Spec:** `../specs/2026-10-09-vless-udp-functional-design.md`, parent Task4 in `2026-10-09-vless-udp-functional.md`; fixed final helper layout defined by `2026-10-10-vless-kernel-packaging.md`.

## Global Constraints

- One source/local-test owner. Before-copy every touched existing file because this worktree contains reviewed WIP. Use apply_patch; no commits/staging/subagents or source replacement from HEAD.
- Do not restart/launch/install the user's App or 9666, touch actual accounts/trust/systemproxy, alter database formats, or publish/sign anything. Test with temporary files and existing isolated bootstrap fixtures.
- The managed helper is exactly `vibermate-xray` adjacent to the real `vibermated` executable (`vibermate-xray.exe` on Windows naming paths). Node/profile data never chooses an executable. Do not search PATH, current directory, user downloads or network.
- Preserve explicit `--vless-executable` as an intentional host/development override; invalid explicit configuration fails with a bounded diagnostic, never silently falls back. Missing automatically discovered helper may leave VLESS unavailable while existing non-VLESS traffic still works; do not pretend presence proves node connectivity.
- Desktop SOCKS binds `127.0.0.1:0`, uses the existing Capture credential authority and actual Host listener/lifecycle. Standalone Server stays empty-disabled unless explicitly configured; Desktop's attached HTTP server does not gain a public SOCKS listener.
- No rework of existing core/UDP authentication, routing, cancellation or shutdown deadlines. Read-only package review may proceed; package source implementation is not concurrent with this task.

## Task 1: Shared daemon discovery and Desktop loopback startup

**Files:** Create `cmd/vibermated/vless_executable.go`, `vless_executable_test.go`; modify `cmd/vibermated/main.go`, its existing argument tests only as needed, `ui/flutter_app/lib/core/bootstrap/desktop_runtime.dart`, and `ui/flutter_app/test/desktop_runtime_start_test.dart`. Root's next task will deliver host-owned addresses/status and ManualCapture UI; do not modify capture grants, control DTOs or the package builder here.

**Interface:**

```go
// resolveVLESSExecutable resolves a host-controlled override or fixed sibling.
// Empty,nil means the unbundled daemon has no managed kernel. It never searches
// PATH/downloads or falls back after an explicitly configured path fails.
func resolveVLESSExecutable(explicitPath, daemonExecutable string) (string, error)
```

Use `os.Executable()` at each actual entry path and this helper before constructing/running the Host. Resolve the daemon's symbolic launch path to its real installed path, then choose its direct sibling. Require the helper itself to be a regular non-symbolic executable file and a clean absolute path. A deliberate explicit override must be clean absolute and similarly validated; permit normal platform temporary-parent aliases by canonicalizing parents, without following a helper symlink. Return a canonical absolute path. On Unix require executable permission bits; platform-specific mode handling may use `runtime.GOOS` without running a foreign binary. Do not execute `version` at startup or add a checksum that wrongly compares signed bytes with pre-sign metadata: package verification and OS signatures establish distribution identity, this helper resolves the trusted host path.

- [ ] **Step 1 — behavioral RED.** Use real temporary directories/files to demonstrate current empty executable delivery despite a valid packaged sibling, explicit invalid-path acceptance, or missing Desktop SOCKS argument. For a new helper begin with a running minimal implementation, then assert behavior, not only an undefined-symbol error. Add a minimal synthetic executable fixture in the allowed Dart test file, recording argv through the actual `DesktopRuntime.start/_launch` with explicit temporary cache/data paths; assert exactly one `--socks-listen=127.0.0.1:0`. A controlled malformed/EOF bootstrap must terminate and join the owned child; no real daemon/App or generic process framework. Preserve existing transaction-selection/cancel tests.

```go
func TestResolveVLESSExecutableFindsPackagedSibling(t *testing.T) {
    root := t.TempDir()
    daemon := filepath.Join(root, "vibermated")
    kernel := filepath.Join(root, "vibermate-xray")
    if err := os.WriteFile(daemon, []byte("fixture"), 0755); err != nil { t.Fatal(err) }
    if err := os.WriteFile(kernel, []byte("fixture"), 0755); err != nil { t.Fatal(err) }
    expected, err := filepath.EvalSymlinks(kernel)
    if err != nil { t.Fatal(err) }
    got, err := resolveVLESSExecutable("", daemon)
    if err != nil || got != expected { t.Fatalf("got %q, err %v", got, err) }
}
```

- [ ] **Step 2 — centralize discovery.** Implement the helper in its focused file; both Desktop and Server entry paths use the result for `options.Host.Runtime.VLESSExecutable`. Preserve their existing `--vless-executable` parse contract. Reuse existing `os.Executable()` lookup when possible. Missing automatic candidate returns empty,nil; malformed configured path or a present invalid sibling returns a descriptive bounded error without dumping full environment or node secrets. Existing non-VLESS unbundled development/tests can still start.
- [ ] **Step 3 — Desktop SOCKS startup.** Add the literal loopback argument in `DesktopRuntime._launch`'s existing typed argv list, with no shell interpolation or external settings mutation. Existing Host obtains and owns the actual ephemeral port. Keep the existing bootstrap descriptor/schema, HTTP proxy behavior and remote server option unchanged.
- [ ] **Step 4 — targeted verification.** Go cases: valid fixed sibling; daemon launch symlink resolves to real package; missing optional sibling; explicit valid override; relative/unclean explicit override; empty override selects automatic discovery; present directory/symbolic/non-executable helper; explicit invalid override does not fall back to a valid sibling; fixed filename not chosen from PATH/cwd. Check bounded error contents. Use native Unix files for native assertions and cross-compile-only labels where relevant. Dart fixture verifies the exact new argument plus unchanged existing bootstrap/start/cancel behavior, using its synthetic process only.

```sh
/tmp/vibermate-go-security.JcRyfL/go/bin/go test ./cmd/vibermated -run 'TestResolveVLESSExecutable|TestParse' -count=1
/opt/homebrew/bin/flutter test test/desktop_runtime_start_test.dart
```

Run Go from repo root and Flutter from `ui/flutter_app`. Also format/analyze only the touched Dart scope and gofmt changed Go. The existing test file covers selection transactions, not an argv-recording process; its new fixture is deliberately scoped to this missing boundary. Do not launch a live App, whole-project/race/soak suite or rebuild the kernel. If an existing fixture has a real regression, preserve the first failure and fix the affected behavior, not its assertions away.
- [ ] **Step 5 — freeze.** Retain before-based full diff, exact source hashes, RED/GREEN commands/output, real temporary cleanup and terminal handles in this plan's SDD report. Release source/local-test slot. Root dispatches independent spec+quality review; no commits or reviewers from the implementer.

## Self-review and remaining user entry

| Seam | Agreement |
| --- | --- |
| package → Go daemon | exact sibling name/layout; both host modes share one resolver |
| explicit override → discovery | invalid explicit input never selects a different helper |
| unbundled daemon → non-VLESS runtime | missing optional helper does not break unrelated traffic or claim VLESS ready |
| Desktop argv → existing Host listener | authenticated IPv4 loopback, actual ephemeral port, existing lifecycle |
| Server/attached HTTP → SOCKS | no new public exposure by default |

Remaining parent work: project actual host-owned kernel availability/SOCKS endpoint through authenticated control and separate ManualCapture connection delivery, with strict Dart/CLI consumers and a real assembled REALITY/Vision TCP/UDP demonstration. This bootstrap task does not claim those paths or signed distribution are done.
