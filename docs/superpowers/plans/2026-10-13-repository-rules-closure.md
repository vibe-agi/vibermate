# B5 Repository Rule Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the three demonstrated repository-check failures without deleting historical evidence, exempting a module, or changing network behavior.

**Architecture:** Keep the existing public checker and composition walk. Exclude only the root SDD evidence subtree, recognize only the two existing private-core dial expressions under their actual Client methods, and preserve Unicode test values with ASCII source escapes. This is one independently reviewable rules/test correction, not an egress redesign.

**Tech Stack:** Go 1.26.9, go/ast and go/parser, existing repositorycheck fixture helpers.

**Spec:** `docs/superpowers/plans/2026-10-09-production-continuation.md` B5; source diagnosis `.superpowers/sdd/2026-10-09-production-continuation/b5-repositorycheck-diagnosis-b4249020.md`. The retained actual failure is `.superpowers/sdd/2026-10-12-native-egress-closure/task-1-repositorycheck-1.log`.

## Global Constraints

- Execute after B3/B4 close; reconcile against that execution HEAD. Preparation pin is `4bd819601e3cbf270a8a0eac3e0fd454f4e56762`.
- Preserve all first failures, SDD snapshots and unrelated work. Root alone stages/commits. One source writer and one heavy test process at a time.
- No whole-file/package VLESS exemption; no new external target permission, proxy fallback, trust exception, dependency, user state or product network change.
- Skip only root `.superpowers/sdd`, not `.superpowers`, all hidden directories, similarly named siblings, or a nested directory elsewhere.
- Real private fields currently originate from an allocated 127.0.0.1 TCP4 listener and private temporary Unix socket; actual source ownership must remain unchanged. The syntactic check is not a whole-program taint/provenance proof.
- This is the finite existing B5 rules gate. It does not claim installed-client behavior, qualify a candidate, or introduce Phase C requirements.

---

### Task 1: Preserve evidence and precisely recognize private-core wiring

**Files:**

- Modify: `internal/repositorycheck/composition.go` (productionGoFiles only).
- Modify: `internal/repositorycheck/check.go` (CheckExternalEgressGate only).
- Create: `internal/repositorycheck/private_core_dial.go` (narrow AST recognition).
- Create: `internal/repositorycheck/egress_rule_closure_test.go` (focused positive/negative fixtures).
- Modify: `internal/egressnetwork/datagram_test.go` (Unicode string spelling only).

**Interfaces:** Reuse `writeAcceptanceRuleFixture(t, root, path, source)`, `CheckProductionCompositionBoundary(root)`, `CheckExternalEgressGate(root)` and the public `Check(root)`. Add only the private helper `privateCoreDialSymbols(relative string, parsed *ast.File, imports map[string]string) map[*ast.SelectorExpr]bool`. No product API changes.

- [ ] **Step 1: Add failing scope tests using actual checker entry points.**

Create `TestCompositionSDDEvidenceExclusionIsExact`. For each path below, write the same `serverhost.Start()` call, with the real holder and daemon fixture from `TestNativeAcceptanceCompositionAllowancePreservesIssuerBoundary`. Only the first row may have no violation:

```go
paths := []struct { path string; archived bool }{
    {".superpowers/sdd/run/cmd/copied/main.go", true},
    {".superpowers/live/main.go", false},
    {".superpowers/sdd-copy/main.go", false},
    {"other/.superpowers/sdd/main.go", false},
    {"cmd/secondentry/main.go", false},
    {"internal/sneakyhost/host.go", false},
}
source := "package copied; import \"github.com/vibe-agi/vibermate/internal/serverhost\"; func f(){ serverhost.Start() }"
```

Require rejected rows to report `unreviewed-server-host-composition` at their exact path, not merely any error. Preserve the existing public good/bad composition tests unchanged.

Create `TestPrivateCoreDialAllowanceIsExact`. The two positive source fixtures use the actual receiver, method, network and field:

```go
tcpSource := "package vlesscore; import (\"context\"; \"net\"); type Client struct{ endpoint string }; func (c *Client) authenticate(ctx context.Context)(net.Conn,error){ return (&net.Dialer{}).DialContext(ctx,\"tcp4\",c.endpoint) }"
udpSource := "package vlesscore; import (\"context\"; \"net\"); type Client struct{ udpPath string }; func (c *Client) DialUDP(ctx context.Context)(net.Conn,error){ return (&net.Dialer{}).DialContext(ctx,\"unix\",c.udpPath) }"
```

Write TCP only at `internal/vlesscore/socks.go`, UDP only at `internal/vlesscore/datagram.go`. Require zero violations for each positive. Mutate one dimension per fresh fixture and require the specific `external-egress-gate` net.Dialer violation: network, caller-supplied address, receiver type Other, receiver-less function, wrong method, same-spelled shadowed receiver, call in a nested function literal, sibling source path, nonempty Dialer literal, extra raw Dialer in the same allowed method/file, and a separate http.Client. Include a named net import alias positive and dot-import negative; use imports map identity, not an assumed identifier spelling.

- [ ] **Step 2: Preserve the RED.**

Run only the two new test names with the pinned command prefix below, `-count=1 -v ./internal/repositorycheck -run '^(TestCompositionSDDEvidenceExclusionIsExact|TestPrivateCoreDialAllowanceIsExact)$'`. Save verbatim stdout/stderr, exact source hashes, command and exit status in a unique SDD run. Expected RED is the archived composition copy plus the two legitimate private dials being rejected; malformed fixtures or compile errors are not behavioral RED.

- [ ] **Step 3: Apply the minimal scanner correction.**

Inside the directory branch of productionGoFiles, before parsing source, compute the root-relative path and skip only this exact subtree:

```go
relative, err := filepath.Rel(repositoryRoot, path)
if err != nil { return err }
if filepath.ToSlash(relative) == ".superpowers/sdd" {
    // Retained review evidence is not shipped production composition.
    return filepath.SkipDir
}
```

In `privateCoreDialSymbols`, return an empty map unless file path and package match. Select only method `authenticate` in socks.go or `DialUDP` in datagram.go; require one named receiver whose type is `*Client` and has a nonnil parser-resolved ast.Object. Inspect that method body but do not descend into nested function literals. Match only a three-argument `DialContext` call whose receiver is a parenthesized address-of empty `net.Dialer{}` literal. Resolve the literal type's import through the named-import map. Require the exact interpreted network string (`tcp4` or `unix`) and third argument field (`endpoint` or `udpPath`) on an identifier with the very same ast.Object as the method receiver. Return only that literal's exact `*ast.SelectorExpr` node, not its method, file or descendants.

After building imports in CheckExternalEgressGate, compute that map once and bypass only an exact matched selector inside the existing protected-symbol inspection:

```go
privateDials := privateCoreDialSymbols(relative, parsed, imports)
// In the existing protected-selector branch:
if privateDials[selector] { return true }
```

All other protected symbols and existing public allowlists stay unchanged. If parser object binding differs from the pinned source assumption, demonstrate the actual AST behavior with the focused fixture before revising the helper; do not replace binding identity with a string-name allowance.

- [ ] **Step 4: Keep Unicode behavior while fixing source spelling.**

Change only the literals in TestDirectDomainsCompactIdentityEdges:

```go
{"B\u00dcCHER.example.", "xn--bcher-kva.example", true},
{"b\u00fccher.example", "A.B\u00dcCHER.example.", true},
```

The runtime strings must remain byte-identical to the prior source. Run this actual test; do not weaken the English-source rule.

- [ ] **Step 5: Verify once on the final amended source.**

Pinned prefix:

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go
```

Run serially (append each command below to that prefix):

```sh
test -count=1 -timeout=180s ./internal/repositorycheck -v
test -count=1 -timeout=180s ./internal/egressnetwork -run '^TestDirectDomainsCompactIdentityEdges$' -v
run ./cmd/repositorycheck
```

Also run gofmt on owned Go files and `git diff --check`. The full checker package includes the new focused positives/negatives and existing public wiring, so do not duplicate all those cohorts again. Save genuine unique process logs, before/after source hashes and exits. A remaining repository check failure is reported with exact path/rule and minimum closure condition; no broad exemption.

No VLESS product or transport source changes occur, so do not rerun its previously accepted core matrix merely for an AST rule edit. The B5 candidate's actual locked-helper/installed validation remains required by the existing release plan. If product changes become necessary, stop for a scope ruling.

- [ ] **Step 6: Review, then root commit.**

Report exact RED/GREEN, scope-negative coverage, Unicode byte equivalence, unchanged private endpoint provenance and explicit static-analysis limitations. Root generates the complete five-file diff, including untracked files, then dispatches one independent spec/security/quality review. Fix only findings/affected consumers; preserve failures. Root stages only these owned paths plus this plan after clean review. Do not mark B5 or the Goal complete: frozen candidate validation and public delivery follow.

## Plan self-review

All three actual failure classes are covered by one bounded unit. No evidence deletion, blanket allowlist, new networking permission or production dialect change is planned. AST object binding is tested rather than assumed; pointer-identity allowance applies to one concrete expression. Existing public rule wiring stays independently exercised by its package tests. Preparation only: no checker rerun or source correction has happened. Execution waits for B3/B4 and uses their actual accepted HEAD.
