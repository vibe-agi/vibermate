package toolpolicy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type reviewDouble struct {
	calls int
}

func (review *reviewDouble) Decide(
	context.Context,
	exchange.ToolDecisionRequest,
) (exchange.ToolDecision, error) {
	review.calls++
	return exchange.ToolDecision{Outcome: exchange.ToolDecisionApproved}, nil
}

func TestObserveNeverInterruptsUnknownTools(t *testing.T) {
	review := &reviewDouble{}
	gate, err := New(review)
	if err != nil {
		t.Fatal(err)
	}
	request := decisionRequest(t, environment.ToolPolicyObserve, "", false, "Bash", `{"command":"rm -rf ."}`, "command")
	decision, err := gate.Decide(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != exchange.ToolDecisionApproved || review.calls != 0 {
		t.Fatalf("decision=%+v reviewCalls=%d", decision, review.calls)
	}
}

func TestReviewAllowsOnlyProvenStructuredWorkspaceActions(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	review := &reviewDouble{}
	gate, err := New(review)
	if err != nil {
		t.Fatal(err)
	}
	request := decisionRequest(t, environment.ToolPolicyReview, root, true, "Read", `{"file_path":"notes.txt"}`, "file_path")
	decision, err := gate.Decide(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != exchange.ToolDecisionApproved || review.calls != 0 {
		t.Fatalf("safe decision=%+v reviewCalls=%d", decision, review.calls)
	}

	request = decisionRequest(t, environment.ToolPolicyReview, root, false, "Read", `{"file_path":"notes.txt"}`, "file_path")
	if _, err := gate.Decide(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if review.calls != 1 {
		t.Fatalf("unverified client review calls=%d", review.calls)
	}
}

func TestReviewDoesNotPromotePathsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	review := &reviewDouble{}
	gate, err := New(review)
	if err != nil {
		t.Fatal(err)
	}
	request := decisionRequest(t, environment.ToolPolicyReview, root, true, "Read", `{"file_path":`+quotedJSON(outsideFile)+`}`, "file_path")
	if _, err := gate.Decide(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if review.calls != 1 {
		t.Fatalf("outside path review calls=%d", review.calls)
	}

	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	request = decisionRequest(t, environment.ToolPolicyReview, root, true, "Read", `{"file_path":"outside-link/secret.txt"}`, "file_path")
	if _, err := gate.Decide(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if review.calls != 2 {
		t.Fatalf("symlink escape review calls=%d", review.calls)
	}
}

func TestReviewDoesNotPromoteWriteThroughDanglingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "new-file.txt")
	if err := os.Symlink(filepath.Join(outside, "new-file.txt"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	review := &reviewDouble{}
	gate, err := New(review)
	if err != nil {
		t.Fatal(err)
	}
	request := decisionRequest(
		t,
		environment.ToolPolicyReview,
		root,
		true,
		"Write",
		`{"file_path":"new-file.txt","content":"hello"}`,
		"file_path",
	)
	if _, err := gate.Decide(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if review.calls != 1 {
		t.Fatalf("dangling symlink review calls=%d", review.calls)
	}
}

func TestReviewRejectsTraversalInAnOptionalWorkspacePattern(t *testing.T) {
	root := t.TempDir()
	review := &reviewDouble{}
	gate, err := New(review)
	if err != nil {
		t.Fatal(err)
	}
	request := decisionRequest(
		t,
		environment.ToolPolicyReview,
		root,
		true,
		"Glob",
		`{"pattern":"../outside/**"}`,
		"path",
	)
	if _, err := gate.Decide(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if review.calls != 1 {
		t.Fatalf("traversal pattern review calls=%d", review.calls)
	}
}

func TestStrictRejectsShellWithoutCreatingApproval(t *testing.T) {
	review := &reviewDouble{}
	gate, err := New(review)
	if err != nil {
		t.Fatal(err)
	}
	request := decisionRequest(t, environment.ToolPolicyStrict, t.TempDir(), true, "Bash", `{"command":"touch allowed.txt"}`, "command")
	decision, err := gate.Decide(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != exchange.ToolDecisionRejected ||
		decision.ReasonCode != ReasonStrictPolicy || review.calls != 0 {
		t.Fatalf("decision=%+v reviewCalls=%d", decision, review.calls)
	}
}

func TestWorkspaceAuthorityPolicyMatrix(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(file, []byte("server notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "notes.txt"), []byte("outside notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside")
	linkErr := os.Symlink(outside, link)
	for _, mode := range []environment.ToolPolicyMode{environment.ToolPolicyObserve, environment.ToolPolicyReview, environment.ToolPolicyStrict} {
		for _, test := range []struct {
			name       string
			root       string
			structured bool
			tool       string
			arguments  string
			field      string
			namespace  string
			proven     bool
		}{
			{"remote-existing-server-file", "", false, "Read", `{"file_path":` + quotedJSON(file) + `}`, "file_path", "", false},
			{"remote-symlink", "", false, "Read", `{"file_path":` + quotedJSON(filepath.Join(link, "notes.txt")) + `}`, "file_path", "", false},
			{"remote-missing-directory", "", false, "Read", `{"file_path":` + quotedJSON(filepath.Join(root, "missing", "notes.txt")) + `}`, "file_path", "", false},
			{"local-safe-file", root, true, "Read", `{"file_path":"notes.txt"}`, "file_path", "", true},
			{"local-symlink-escape", root, true, "Read", `{"file_path":"outside/notes.txt"}`, "file_path", "", false},
			{"local-missing-directory", filepath.Join(root, "missing"), true, "Read", `{"file_path":"notes.txt"}`, "file_path", "", false},
			{"local-unverified-client", root, false, "Read", `{"file_path":"notes.txt"}`, "file_path", "", false},
			{"shell", root, true, "Bash", `{"command":"cat notes.txt"}`, "command", "", false},
			{"MCP-read", root, true, "Read", `{"file_path":"notes.txt"}`, "file_path", "mcp", false},
		} {
			t.Run(string(mode)+"/"+test.name, func(t *testing.T) {
				if (test.name == "remote-symlink" || test.name == "local-symlink-escape") && linkErr != nil {
					t.Skipf("symlinks unavailable: %v", linkErr)
				}
				review := &reviewDouble{}
				gate, err := New(review)
				if err != nil {
					t.Fatal(err)
				}
				request := decisionRequestWithNamespace(t, mode, test.root, test.structured, test.tool, test.arguments, test.field, test.namespace)
				decision, err := gate.Decide(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				want := exchange.ToolDecisionApproved
				wantReview := 0
				wantReason := ""
				if !test.proven && mode == environment.ToolPolicyReview {
					wantReview = 1
				}
				if !test.proven && mode == environment.ToolPolicyStrict {
					want = exchange.ToolDecisionRejected
					wantReason = ReasonStrictPolicy
				}
				if decision.Outcome != want || decision.ReasonCode != wantReason || review.calls != wantReview {
					t.Fatalf("decision=%+v reviewCalls=%d; want outcome=%s reason=%q reviewCalls=%d", decision, review.calls, want, wantReason, wantReview)
				}
			})
		}
	}
}

func decisionRequest(
	t *testing.T,
	mode environment.ToolPolicyMode,
	root string,
	structured bool,
	name string,
	argumentsJSON string,
	pathField string,
) exchange.ToolDecisionRequest {
	t.Helper()
	return decisionRequestWithNamespace(t, mode, root, structured, name, argumentsJSON, pathField, "")
}

func decisionRequestWithNamespace(
	t *testing.T,
	mode environment.ToolPolicyMode,
	root string,
	structured bool,
	name string,
	argumentsJSON string,
	pathField string,
	namespace string,
) exchange.ToolDecisionRequest {
	t.Helper()
	schema, err := protocolcore.NewJSONObject([]byte(`{"type":"object","properties":{"`+pathField+`":{"type":"string"}},"required":["`+pathField+`"]}`), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := protocolcore.NewJSONObject([]byte(argumentsJSON), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	key, err := protocolcore.NewCallKey("anthropic-messages", "tool-call")
	if err != nil {
		t.Fatal(err)
	}
	decisionContext, err := exchange.NewToolDecisionContext(
		environment.PolicySet{ToolMode: mode}, root, structured,
		[]protocolcore.ToolDefinition{{Name: name, InputSchema: schema}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	environmentID, err := environment.NewEnvironmentID("environment-tools")
	if err != nil {
		t.Fatal(err)
	}
	routeID, err := environment.NewUpstreamRouteID("route-tools")
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewToolDecisionRequest(
		"exchange-tools", environmentID, 1, environment.CandidateDigest{1},
		routeID, 1, decisionContext,
		[]protocolcore.ToolIntent{{
			ResponseID: "response-tools", Call: protocolcore.ToolCall{
				Key: key, Name: name, Arguments: arguments, Namespace: namespace,
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func quotedJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
