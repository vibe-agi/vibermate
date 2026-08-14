package agentconversation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestCodexIdentityResolverGroupsTurnsByExactAgentThread(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexRollout(t, filepath.Join(root, "2026", "08", "14", "rollout-subagent.jsonl"), []string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"session_id":"session-root","id":"thread-reviewer","forked_from_id":"session-root","parent_thread_id":"session-root","cwd":%q,"originator":"codex-tui","cli_version":"0.150.0","source":{"subagent":{"thread_spawn":{"parent_thread_id":"session-root","depth":1,"agent_path":"/root/code_review","agent_nickname":"Reviewer","agent_role":"reviewer"}}},"thread_source":"subagent","agent_nickname":"Reviewer","agent_path":"/root/code_review","agent_role":"reviewer","model_provider":"openai","context_window":{"window_id":"window-reviewer"}}}`, workspace),
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"type":"turn_context","payload":{"turn_id":"turn-1","comp_hash":"comp-turn-1"}}`,
		`{"type":"response_item","payload":{"type":"reasoning","id":"rs-1","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}}}`,
		`{"type":"response_item","payload":{"type":"function_call","id":"fc-1","call_id":"call-1","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}}}`,
		`{"type":"compacted","payload":{"window_id":"window-2","previous_window_id":"window-1","first_window_id":"window-1"}}`,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-2"}}`,
		`{"type":"response_item","payload":{"type":"message","id":"msg-2","internal_chat_message_metadata_passthrough":{"turn_id":"turn-2"}}}`,
	})
	resolver, err := NewCodexIdentityResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	resolved, err := resolver.ResolveBatch(context.Background(), workspace, []ClientIdentityLookup{
		codexTestLookup("response-1", "session-root", "thread-reviewer", "turn-1", observed),
		codexTestLookup("response-2", "session-root", "thread-reviewer", "turn-2", observed.Add(time.Second)),
	})
	if err != nil {
		t.Fatalf("ResolveBatch() error = %v", err)
	}
	first, firstFound := resolved["response-1"]
	second, secondFound := resolved["response-2"]
	if !firstFound || !secondFound {
		t.Fatalf("resolved identities = %#v", resolved)
	}
	if first.SessionID != "session-root" || !first.SessionResumable ||
		first.ActorID != "thread-reviewer" || first.ActorLabel != "Reviewer" ||
		first.ActorType != "reviewer" || !first.ActorIsSubagent ||
		second.ActorID != first.ActorID {
		t.Fatalf("resolved identities = %#v / %#v", first, second)
	}
	for name, value := range map[string]string{
		"codex.session_id":           "session-root",
		"codex.thread_id":            "thread-reviewer",
		"codex.turn_id":              "turn-1",
		"codex.parent_thread_id":     "session-root",
		"codex.context_window_id":    "window-reviewer",
		"codex.compaction_window_id": "window-2",
		"codex.previous_window_id":   "window-1",
		"codex.first_window_id":      "window-1",
		"codex.compaction_hash":      "comp-turn-1",
		"codex.reasoning_item_id":    "rs-1",
		"codex.call_id":              "call-1",
	} {
		if !hasClientEvidence(first.ProtocolIDs, name, value) {
			t.Fatalf("protocol IDs = %#v, want %s=%s", first.ProtocolIDs, name, value)
		}
	}
	for name, value := range map[string]string{
		"codex.agent_path":     "/root/code_review",
		"codex.agent_nickname": "Reviewer",
		"codex.agent_role":     "reviewer",
		"codex.spawn_depth":    "1",
	} {
		if !hasClientEvidence(first.Attributes, name, value) {
			t.Fatalf("attributes = %#v, want %s=%s", first.Attributes, name, value)
		}
	}
	mainRef, err := Project(ProjectionInput{
		CaptureRunID: "run-1", ExchangeID: "exchange-1", ClientIdentity: &first,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondRef, err := Project(ProjectionInput{
		CaptureRunID: "run-1", ExchangeID: "exchange-2", ClientIdentity: &second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mainRef.ProjectionID != secondRef.ProjectionID || mainRef.DisplayName != "Reviewer" {
		t.Fatalf("conversation refs = %#v / %#v", mainRef, secondRef)
	}
}

func TestCodexIdentityResolverRefusesMissingOrMismatchedExactEvidence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexRollout(t, filepath.Join(root, "rollout-main.jsonl"), []string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"session_id":"session-main","id":"thread-main","cwd":%q,"source":"cli","thread_source":"user"}}`, workspace),
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-main"}}`,
	})
	resolver, err := NewCodexIdentityResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC()
	lookups := []ClientIdentityLookup{
		{
			ProviderResponseID: "missing-turn", ObservedAt: observed,
			ProtocolEvidence: []protocolcore.ProtocolEvidenceValue{{Name: "openai_responses.session_id", Value: "session-main"}},
		},
		codexTestLookup("wrong-thread", "session-main", "another-thread", "turn-main", observed),
	}
	resolved, err := resolver.ResolveBatch(context.Background(), workspace, lookups)
	if err != nil {
		t.Fatalf("ResolveBatch() error = %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("resolved identities = %#v, want none", resolved)
	}
}

func TestCodexIdentityResolverAcceptsLegacyRootAndIsolatesInvalidRollout(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexRollout(t, filepath.Join(root, "rollout-invalid.jsonl"), []string{
		`{"type":"session_meta","payload":{"id":"unrelated","cwd":"relative/path","source":"cli"}}`,
	})
	writeCodexRollout(t, filepath.Join(root, "rollout-legacy.jsonl"), []string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":"legacy-session","cwd":%q,"source":"cli"}}`, workspace),
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"legacy-turn"}}`,
	})
	resolver, err := NewCodexIdentityResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.ResolveBatch(context.Background(), workspace, []ClientIdentityLookup{
		codexTestLookup(
			"legacy-response",
			"legacy-session",
			"legacy-session",
			"legacy-turn",
			time.Now().UTC(),
		),
	})
	if err != nil {
		t.Fatalf("ResolveBatch() error = %v", err)
	}
	identity, found := resolved["legacy-response"]
	if !found || identity.SessionID != "legacy-session" || identity.ActorID != "" ||
		!hasClientEvidence(identity.ProtocolIDs, "codex.session_id", "legacy-session") ||
		!hasClientEvidence(identity.ProtocolIDs, "codex.thread_id", "legacy-session") {
		t.Fatalf("resolved identity = %#v", identity)
	}
}

func TestCodexIdentityResolverCanonicalizesWorkspaceAndSkipsUnrelatedBody(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	realWorkspace := filepath.Join(root, "real-workspace")
	if err := os.Mkdir(realWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceAlias := filepath.Join(root, "workspace-alias")
	if err := os.Symlink(realWorkspace, workspaceAlias); err != nil {
		t.Fatal(err)
	}
	unrelatedWorkspace := filepath.Join(root, "unrelated")
	if err := os.Mkdir(unrelatedWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexRollout(t, filepath.Join(root, "rollout-unrelated.jsonl"), []string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"session_id":"other-session","id":"other-thread","cwd":%q,"source":"exec"}}`, unrelatedWorkspace),
		`{"this trailing payload is deliberately malformed and must not be scanned"`,
	})
	writeCodexRollout(t, filepath.Join(root, "rollout-current.jsonl"), []string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"session_id":"session-current","id":"thread-current","cwd":%q,"source":"exec"}}`, workspaceAlias),
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-current"}}`,
	})
	resolver, err := NewCodexIdentityResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.ResolveBatch(context.Background(), realWorkspace, []ClientIdentityLookup{
		codexTestLookup(
			"response-current",
			"session-current",
			"thread-current",
			"turn-current",
			time.Now().UTC(),
		),
	})
	if err != nil {
		t.Fatalf("ResolveBatch() error = %v", err)
	}
	identity, found := resolved["response-current"]
	if !found || identity.SessionID != "session-current" {
		t.Fatalf("resolved identity = %#v", identity)
	}
}

func TestCodexIdentityResolverDoesNotAttributeCopiedParentHistoryToChildThread(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexRollout(t, filepath.Join(root, "rollout-root.jsonl"), []string{
		fmt.Sprintf(`{"timestamp":"2026-08-14T12:00:00.000Z","type":"session_meta","payload":{"session_id":"session-root","id":"thread-root","cwd":%q,"source":"exec","thread_source":"user"}}`, workspace),
		`{"timestamp":"2026-08-14T12:00:00.001Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-root"}}`,
	})
	writeCodexRollout(t, filepath.Join(root, "rollout-child.jsonl"), []string{
		fmt.Sprintf(`{"timestamp":"2026-08-14T12:01:00.000Z","type":"session_meta","payload":{"session_id":"session-root","id":"thread-child","cwd":%q,"source":{"subagent":{"thread_spawn":{"parent_thread_id":"thread-root","depth":1,"agent_path":"/root/child","agent_nickname":"Child"}}}}}`, workspace),
		fmt.Sprintf(`{"timestamp":"2026-08-14T12:01:00.000Z","type":"session_meta","payload":{"session_id":"session-root","id":"thread-root","cwd":%q,"source":"exec"}}`, workspace),
		`{"timestamp":"2026-08-14T12:01:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-root"}}`,
		`{"timestamp":"2026-08-14T12:01:00.003Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-child"}}`,
	})
	resolver, err := NewCodexIdentityResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Date(2026, 8, 14, 12, 2, 0, 0, time.UTC)
	resolved, err := resolver.ResolveBatch(context.Background(), workspace, []ClientIdentityLookup{
		codexTestLookup("response-root", "", "", "turn-root", observed),
		codexTestLookup("response-child", "", "", "turn-child", observed),
	})
	if err != nil {
		t.Fatalf("ResolveBatch() error = %v", err)
	}
	if resolved["response-root"].ActorID != "" ||
		resolved["response-root"].SessionID != "session-root" ||
		resolved["response-child"].ActorID != "thread-child" ||
		!resolved["response-child"].ActorIsSubagent {
		t.Fatalf("resolved identities = %#v", resolved)
	}
}

func TestCodexIdentityResolverJoinsInitialTurnByExactProviderItemID(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexRollout(t, filepath.Join(root, "rollout-child.jsonl"), []string{
		fmt.Sprintf(`{"timestamp":"2026-08-14T12:00:00.000Z","type":"session_meta","payload":{"session_id":"session-root","id":"thread-child","cwd":%q,"source":{"subagent":{"thread_spawn":{"parent_thread_id":"thread-root","depth":1,"agent_path":"/root/reviewer","agent_nickname":"Reviewer"}}}}}`, workspace),
		`{"timestamp":"2026-08-14T12:00:00.001Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-child"}}`,
		`{"timestamp":"2026-08-14T12:00:00.002Z","type":"response_item","payload":{"type":"reasoning","id":"rs-exact"}}`,
		`{"timestamp":"2026-08-14T12:00:00.003Z","type":"response_item","payload":{"type":"message","id":"msg-exact"}}`,
	})
	resolver, err := NewCodexIdentityResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.ResolveBatch(context.Background(), workspace, []ClientIdentityLookup{{
		ProviderResponseID: "response-network",
		ObservedAt:         time.Date(2026, 8, 14, 12, 0, 1, 0, time.UTC),
		ResponseProtocolEvidence: []protocolcore.ProtocolEvidenceValue{
			{Name: "openai_responses.output.0000.id", Value: "rs-exact"},
			{Name: "openai_responses.output.0001.id", Value: "msg-exact"},
		},
	}})
	if err != nil {
		t.Fatalf("ResolveBatch() error = %v", err)
	}
	identity, found := resolved["response-network"]
	if !found || identity.SessionID != "session-root" ||
		identity.ActorID != "thread-child" || identity.ActorLabel != "Reviewer" ||
		!identity.ActorIsSubagent ||
		!hasClientEvidence(identity.ProtocolIDs, "codex.turn_id", "turn-child") ||
		!hasClientEvidence(identity.ProtocolIDs, "codex.reasoning_item_id", "rs-exact") ||
		!hasClientEvidence(identity.ProtocolIDs, "codex.response_item_id", "msg-exact") {
		t.Fatalf("resolved identity = %#v", identity)
	}
}

func TestCodexIdentityResolverJoinsByProviderTurnMetadata(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexRollout(t, filepath.Join(root, "rollout-child.jsonl"), []string{
		fmt.Sprintf(`{"timestamp":"2026-08-14T12:00:00.000Z","type":"session_meta","payload":{"session_id":"session-root","id":"thread-child","cwd":%q,"source":{"subagent":{"thread_spawn":{"parent_thread_id":"thread-root","depth":1,"agent_path":"/root/reviewer","agent_nickname":"Reviewer"}}}}}`, workspace),
		`{"timestamp":"2026-08-14T12:00:00.001Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-child"}}`,
		`{"timestamp":"2026-08-14T12:00:00.002Z","type":"response_item","payload":{"type":"message","id":"msg-exact","metadata":{"turn_id":"turn-child"}}}`,
	})
	resolver, err := NewCodexIdentityResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.ResolveBatch(context.Background(), workspace, []ClientIdentityLookup{{
		ProviderResponseID: "response-network",
		ObservedAt:         time.Date(2026, 8, 14, 12, 0, 1, 0, time.UTC),
		ResponseProtocolEvidence: []protocolcore.ProtocolEvidenceValue{
			{Name: "openai_responses.output.0000.metadata.turn_id", Value: "turn-child"},
		},
	}})
	if err != nil {
		t.Fatalf("ResolveBatch() error = %v", err)
	}
	identity, found := resolved["response-network"]
	if !found || identity.SessionID != "session-root" ||
		identity.ActorID != "thread-child" || identity.ActorLabel != "Reviewer" ||
		!identity.ActorIsSubagent ||
		!hasClientEvidence(identity.ProtocolIDs, "codex.turn_id", "turn-child") {
		t.Fatalf("resolved identity = %#v", identity)
	}
}

func codexTestLookup(
	responseID, sessionID, threadID, turnID string,
	observed time.Time,
) ClientIdentityLookup {
	evidence := make([]protocolcore.ProtocolEvidenceValue, 0, 3)
	if sessionID != "" {
		evidence = append(evidence, protocolcore.ProtocolEvidenceValue{
			Name: "openai_responses.session_id", Value: sessionID,
		})
	}
	if threadID != "" {
		evidence = append(evidence, protocolcore.ProtocolEvidenceValue{
			Name: "openai_responses.thread_id", Value: threadID,
		})
	}
	if turnID != "" {
		evidence = append(evidence, protocolcore.ProtocolEvidenceValue{
			Name: "openai_responses.turn_id", Value: turnID,
		})
	}
	return ClientIdentityLookup{
		ProviderResponseID: responseID,
		ObservedAt:         observed,
		ProtocolEvidence:   evidence,
	}
}

func writeCodexRollout(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	encoded := ""
	for _, line := range lines {
		encoded += line + "\n"
	}
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasClientEvidence(values []ClientEvidenceValue, name, value string) bool {
	for _, candidate := range values {
		if candidate.Name == name && candidate.Value == value {
			return true
		}
	}
	return false
}
