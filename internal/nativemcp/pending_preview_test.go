package nativemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPendingPreviewZeroesOnRevocationCloseAndExpiry(t *testing.T) {
	for _, ending := range []string{"revoke", "close", "expiry"} {
		t.Run(ending, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			e := f.executor.(*executor)
			e.dependencies.ContentProfiles = syntheticProfiles{}
			f.handshake(t)
			call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"temporary"}}}`, "synthetic-session")
			result := make(chan Terminal, 1)
			go func() { result <- e.Serve(context.Background(), call, httptest.NewRecorder()) }()
			a := waitNativeQuestion(t, f, call.id)
			e.mu.Lock()
			owned := e.previews[call.id].data
			e.mu.Unlock()
			switch ending {
			case "revoke":
				if err := e.Revoke(context.Background(), f.capture, RevocationCaptureStopped); err != nil {
					t.Fatal(err)
				}
			case "close":
				if err := e.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-result:
			case <-time.After(4 * time.Second):
				t.Fatal("pending decision did not terminate")
			}
			for _, b := range owned {
				if b != 0 {
					t.Fatal("owned preview bytes were not zeroed")
				}
			}
			if _, err := e.PendingPreview(context.Background(), f.capture, call.id, a.BindingDigest); err == nil {
				t.Fatal("terminated preview remains available")
			}
			if f.transport.sends() != 3 {
				t.Fatal("terminated pending call was sent")
			}
		})
	}
}
func TestPendingPreviewCapacityDeniesWithoutEvictingPendingIntent(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	e := f.executor.(*executor)
	e.dependencies.ContentProfiles = syntheticProfiles{}
	f.handshake(t)
	const wire = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"retained"}}}`
	call := f.captured(t, "POST", wire, "synthetic-session")
	results := make(chan Terminal, 1)
	go func() { results <- e.Serve(context.Background(), call, httptest.NewRecorder()) }()
	a := waitNativeQuestion(t, f, call.id)
	e.mu.Lock()
	var s *session
	for _, candidate := range e.scopes[f.capture.Key()].sessions {
		s = candidate
	}
	e.mu.Unlock()
	r, err := parseRequest([]byte(wire), e.limits)
	if err != nil {
		t.Fatal(err)
	}
	r.retention = &retentionState{}
	binding, intents, _, err := e.bindRequest(call, r, s)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 1024; i++ {
		id, _ := nativetool.NewID(fmt.Sprintf("%032x", 10000+i))
		if err = e.retainPreview(id, binding, intents, r, s); err != nil {
			t.Fatalf("capacity denied before1024: %v", err)
		}
	}
	id, _ := nativetool.NewID(fmt.Sprintf("%032x", 20000))
	if err = e.retainPreview(id, binding, intents, r, s); err != errBudget {
		t.Fatalf("capacity accepted %v", err)
	}
	if _, err = e.PendingPreview(context.Background(), f.capture, call.id, a.BindingDigest); err != nil {
		t.Fatalf("existing pending preview evicted: %v", err)
	}
	if err = e.Revoke(context.Background(), f.capture, RevocationCaptureStopped); err != nil {
		t.Fatal(err)
	}
	<-results
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.previews) != 0 || e.previewBytes != 0 {
		t.Fatal("revoke retained capacity")
	}
}

func waitNativeQuestion(t *testing.T, f *nativeFixture, id nativetool.ID) nativetool.Approval {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		op, err := f.store.NativeToolRepository().Get(context.Background(), id)
		if err == nil && op.ApprovalID != nil {
			a, err := f.store.NativeToolRepository().GetApproval(context.Background(), *op.ApprovalID)
			if err != nil {
				t.Fatal(err)
			}
			return a
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("native question did not appear")
	return nativetool.Approval{}
}

func TestPendingPreviewTotalByteBudget(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	e := f.executor.(*executor)
	e.dependencies.ContentProfiles = syntheticProfiles{}
	f.handshake(t)
	wire := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"` + strings.Repeat("x", 50<<10) + `"}}}`
	call := f.captured(t, "POST", wire, "synthetic-session")
	defer call.lease.Release()
	e.mu.Lock()
	var session *session
	for _, candidate := range e.scopes[f.capture.Key()].sessions {
		session = candidate
	}
	e.mu.Unlock()
	r, err := parseRequest([]byte(wire), e.limits)
	if err != nil {
		t.Fatal(err)
	}
	r.retention = &retentionState{}
	binding, intents, _, err := e.bindRequest(call, r, session)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.retainPreview(call.id, binding, intents, r, session); err != nil {
		t.Fatal(err)
	}
	first := e.previews[call.id].data
	denied := false
	for index := 0; index < 1024; index++ {
		id, _ := nativetool.NewID(fmt.Sprintf("%032x", 30000+index))
		err = e.retainPreview(id, binding, intents, r, session)
		if err == errBudget {
			denied = true
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !denied || len(e.previews) >= 1024 || e.previewBytes > 16<<20 || e.previewBytes < (16<<20)-(64<<10) {
		t.Fatalf("total byte bound: entries%d bytes%d denied%v", len(e.previews), e.previewBytes, denied)
	}
	if e.previews[call.id] == nil || len(first) == 0 || first[0] != '[' {
		t.Fatal("pressure evicted the first preview")
	}
	if err = e.Revoke(context.Background(), f.capture, RevocationCaptureStopped); err != nil {
		t.Fatal(err)
	}
	for _, value := range first {
		if value != 0 {
			t.Fatal("revocation did not zero owned bytes")
		}
	}
}
func TestPendingPreviewFrozenOffAndLifecycle(t *testing.T) {
	for _, classification := range []string{"known", "unknown", "sensitive", "large"} {
		t.Run(classification, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			if classification != "unknown" {
				f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{argumentPaths: []string{"/proof"}}
			}
			f.handshake(t)
			args := `{"path":"frozen.txt","_meta":{"private":"meta-sentinel"}}`
			if classification == "sensitive" {
				args = `{"path":"a","proof":"proof-sentinel"}`
			}
			if classification == "large" {
				args = `{"path":"` + strings.Repeat("x", 70<<10) + `"}`
			}
			c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":`+args+`}}`, "synthetic-session")
			results := make(chan Terminal, 1)
			go func() { results <- f.executor.Serve(context.Background(), c, httptest.NewRecorder()) }()
			a := waitNativeQuestion(t, f, c.id)
			preview, err := f.executor.PendingPreview(context.Background(), f.capture, c.id, a.BindingDigest)
			if classification == "unknown" || classification == "sensitive" {
				if err == nil {
					t.Fatalf("unclassified preview exposed %+v", preview)
				}
			} else {
				if err != nil || len(preview.Intents) != 1 || preview.Revision != a.Revision {
					t.Fatalf("preview %+v %v", preview, err)
				}
				bytes, _ := json.Marshal(preview)
				if strings.Contains(string(bytes), "sentinel") || strings.Contains(string(bytes), "synthetic-original-login") {
					t.Fatal("private metadata exposed")
				}
				if classification == "known" && string(preview.Intents[0].Arguments) != `{"path":"frozen.txt"}` {
					t.Fatalf("frozen args %s", preview.Intents[0].Arguments)
				}
				if classification == "large" && (!preview.Intents[0].Truncated || len(preview.Intents[0].Arguments) > 0) {
					t.Fatal("large parameters not explicitly truncated")
				}
				preview.Intents[0].Arguments = []byte(`{"path":"changed"}`)
				again, err := f.executor.PendingPreview(context.Background(), f.capture, c.id, a.BindingDigest)
				if err != nil || string(again.Intents[0].Arguments) == `{"path":"changed"}` {
					t.Fatal("preview caller mutated executor")
				}
			}
			if _, err = f.store.NativeToolRepository().DecideApproval(context.Background(), nativetool.DecisionInput{ApprovalID: a.ID, ExpectedRevision: a.Revision, Decision: nativetool.DecisionDeny, IdempotencyKey: "preview-deny-test-key", ObservedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			<-results
			if _, err = f.executor.PendingPreview(context.Background(), f.capture, c.id, a.BindingDigest); err == nil {
				t.Fatal("decided preview survived")
			}
			e := f.executor.(*executor)
			e.mu.Lock()
			count, used := len(e.previews), e.previewBytes
			e.mu.Unlock()
			if count != 0 || used != 0 {
				t.Fatalf("owned preview remains %d/%d", count, used)
			}
			var contents int
			if err = f.db.QueryRow("SELECT count(*) FROM native_tool_contents").Scan(&contents); err != nil || contents != 0 {
				t.Fatalf("OFF content %d %v", contents, err)
			}
		})
	}
}
func TestPinnedNativeInitializeVersionsMatchImmutableSources(t *testing.T) {
	data, err := os.ReadFile("testdata/pinned_clients.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Profiles []struct {
			Version, Commit, SDK string
			SDKCommit            string `json:"sdk_commit"`
		}
	}
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	expected := []struct{ file, version, commit string }{
		{"codex-0.145-initialize.synthetic.json", "0.145.0", "25af12f7e61572b0bc18ddb1008be543b91519b0"},
		{"codex-0.159-initialize.synthetic.json", "0.159.2", "ff6aec96948b70d94983af2641a6b67c94faeff5"},
	}
	if len(manifest.Profiles) != 3 || manifest.Profiles[2].Version != "0.160.0" || manifest.Profiles[2].Commit != "a956835d020762cb2b570053af06f643a11c0ecc" {
		t.Fatal("0.160 immutable source tuple mismatch")
	}
	for i, want := range expected {
		data, err := os.ReadFile("testdata/" + want.file)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Params struct {
				ProtocolVersion string
				ClientInfo      struct{ Name, Title, Version string }
			}
		}
		if err = json.Unmarshal(data, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.Params.ProtocolVersion != "2025-06-18" || wire.Params.ClientInfo.Name != "codex-mcp-client" || wire.Params.ClientInfo.Title != "Codex" {
			t.Errorf("initialize identity/protocol mismatch %s", want.file)
		}
		if manifest.Profiles[i].Version != want.version || manifest.Profiles[i].Commit != want.commit || wire.Params.ClientInfo.Version != want.version {
			t.Errorf("pin %s: manifest version %s initialize %s, want %s", want.commit, manifest.Profiles[i].Version, wire.Params.ClientInfo.Version, want.version)
		}
	}
	for i, p := range manifest.Profiles {
		sdk, commit := "rmcp 3.2.0", "51ccb42993d6eb5075399672ce7a0c21a0e55eea"
		if i == 0 {
			sdk, commit = "rmcp 1.8.0", "25220361d5540715294c501c289d79de4bec2bfc"
		}
		if p.SDK != sdk || p.SDKCommit != commit {
			t.Errorf("SDK source mismatch for%s", p.Version)
		}
	}
}
