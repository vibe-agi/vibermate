package agentconversation_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/agentconversation"
)

func TestClaudeIdentityResolverGroupsOneNativeAgentAcrossProviderMessages(t *testing.T) {
	t.Parallel()

	projectsRoot := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "claude-workspace")
	projectRoot := filepath.Join(
		projectsRoot,
		strings.NewReplacer("/", "-", `\`, "-").Replace(filepath.Clean(workspace)),
	)
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projectRoot, "agent-ace66aeb17c2cf4d.jsonl")
	lines := []map[string]any{
		{
			"sessionId": "session-root", "agentId": "ace66aeb17c2cf4d",
			"uuid": "source-tool-one", "isSidechain": true,
			"attributionAgent": "general-purpose",
			"message": map[string]any{
				"id":      "msg-source-tool-one",
				"content": []map[string]any{{"type": "tool_use", "id": "tool-block-one"}},
			},
		},
		{
			"sessionId": "session-root", "uuid": "parent-one",
			"parentUuid": "source-tool-one", "promptId": "prompt-one",
			"sourceToolAssistantUUID": "source-tool-one",
			"attachment":              map[string]any{"toolUseID": "attachment-tool-one"},
			"toolUseResult":           map[string]any{"agentId": "spawned-agent-one"},
			"message": map[string]any{
				"content": []map[string]any{{"type": "tool_result", "tool_use_id": "tool-result-one"}},
			},
		},
		{
			"sessionId": "session-root", "agentId": "ace66aeb17c2cf4d",
			"requestId": "request-one", "uuid": "event-one",
			"parentUuid": "parent-one", "isSidechain": true,
			"attributionAgent": "general-purpose",
			"message":          map[string]any{"id": "msg-provider-one"},
		},
		{
			"sessionId": "session-root", "agentId": "ace66aeb17c2cf4d",
			"requestId": "request-two", "uuid": "event-two",
			"parentUuid": "parent-two", "isSidechain": true,
			"attributionAgent": "general-purpose",
			"message":          map[string]any{"id": "msg-provider-two"},
		},
	}
	encoded := make([]byte, 0, 1024)
	for _, line := range lines {
		value, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, value...)
		encoded = append(encoded, '\n')
	}
	if err := os.WriteFile(transcript, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{
		"agentType": "general-purpose", "description": "code-review",
		"parentAgentId": "main-agent", "spawnDepth": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(transcript, ".jsonl")+".meta.json", metadata, 0o600); err != nil {
		t.Fatal(err)
	}

	resolver, err := agentconversation.NewClaudeIdentityResolver(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Date(2026, 8, 14, 1, 2, 3, 456000000, time.UTC)
	resolved, err := resolver.ResolveBatch(context.Background(), workspace, []agentconversation.ClientIdentityLookup{
		{ProviderResponseID: "msg-provider-one", ObservedAt: observed},
		{ProviderResponseID: "msg-provider-two", ObservedAt: observed.Add(time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := resolved["msg-provider-one"]
	second := resolved["msg-provider-two"]
	if first.SessionID != "session-root" || !first.SessionResumable ||
		first.ActorID != "ace66aeb17c2cf4d" || first.ActorLabel != "code-review" ||
		first.ActorType != "general-purpose" || !first.ActorIsSubagent ||
		second.ActorID != first.ActorID || second.SessionID != first.SessionID {
		t.Fatalf("Claude identities = %#v / %#v", first, second)
	}
	for name, value := range map[string]string{
		"claude.session_id":                 "session-root",
		"claude.agent_id":                   "ace66aeb17c2cf4d",
		"claude.parent_agent_id":            "main-agent",
		"claude.prompt_id":                  "prompt-one",
		"claude.source_tool_assistant_uuid": "source-tool-one",
		"claude.source_provider_message_id": "msg-source-tool-one",
		"claude.content_block_id":           "tool-block-one",
		"claude.tool_use_id":                "attachment-tool-one",
		"claude.spawned_agent_id":           "spawned-agent-one",
	} {
		if !hasClaudeClientEvidence(first.ProtocolIDs, name, value) {
			t.Fatalf("protocol IDs = %#v, want %s=%s", first.ProtocolIDs, name, value)
		}
	}
	if !hasClaudeClientEvidence(first.Attributes, "claude.description", "code-review") ||
		!hasClaudeClientEvidence(first.Attributes, "claude.spawn_depth", "1") {
		t.Fatalf("Claude attributes = %#v", first.Attributes)
	}

	firstRef, err := agentconversation.Project(agentconversation.ProjectionInput{
		CaptureRunID: "run-one", ExchangeID: "exchange-one",
		SourceDisplayName: "Claude", ClientIdentity: &first,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondRef, err := agentconversation.Project(agentconversation.ProjectionInput{
		CaptureRunID: "run-one", ExchangeID: "exchange-two",
		SourceDisplayName: "Claude", ClientIdentity: &second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstRef.ProjectionID != secondRef.ProjectionID || firstRef.DisplayName != "code-review" {
		t.Fatalf("Claude actor projections = %#v / %#v", firstRef, secondRef)
	}
}

func hasClaudeClientEvidence(
	values []agentconversation.ClientEvidenceValue,
	name string,
	value string,
) bool {
	for _, candidate := range values {
		if candidate.Name == name && candidate.Value == value {
			return true
		}
	}
	return false
}
