package desktopcontrol_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

// The endpoint that answers "what went out" must actually answer it. It used
// to serialize one empty object per attempt, because the attempt keeps its
// fields unexported and had no wire contract.
func TestOutboundAttemptsCarryTheirFields(t *testing.T) {
	t.Parallel()

	fixture := newAuditFixture(t)
	recorded := doRequest(
		t,
		fixture.router,
		fixture.authority,
		http.MethodGet,
		"/api/v1/egress-attempts?limit=20",
		fixture.readToken,
		nil,
	)
	if recorded.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorded.Code, recorded.Body)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(recorded.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) == 0 {
		t.Fatalf("no attempts were returned: %s", recorded.Body)
	}
	attempt := page.Items[0]
	for _, field := range []string{
		"sequence",
		"id",
		"purpose",
		"payloadClass",
		"targetOrigin",
		"decision",
		"startedAt",
		"bytesOut",
		"bytesIn",
	} {
		if _, found := attempt[field]; !found {
			t.Fatalf("attempt is missing %q: %+v", field, attempt)
		}
	}
	if attempt["targetOrigin"] == "" {
		t.Fatalf("attempt carries no target: %+v", attempt)
	}
}

// Design 06 §4.1 bounds what a record may contain. An attempt says where a
// request went and how much crossed, never what it said.
func TestAuditViewsCarryNoRequestContent(t *testing.T) {
	t.Parallel()

	fixture := newAuditFixture(t)
	for _, path := range []string{
		"/api/v1/egress-attempts?limit=20",
		"/api/v1/connections?limit=20",
	} {
		recorded := doRequest(
			t,
			fixture.router,
			fixture.authority,
			http.MethodGet,
			path,
			fixture.readToken,
			nil,
		)
		if recorded.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, recorded.Code)
		}
		body := recorded.Body.String()
		for _, forbidden := range []string{
			"authorization",
			"Authorization",
			"proxy-authorization",
			"\"path\"",
			"\"headers\"",
			"\"body\"",
			"sk-",
		} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s leaked %q: %s", path, forbidden, body)
			}
		}
	}
}

type auditFixture struct {
	router     http.Handler
	authority  string
	readToken  string
	writeToken string
	runtime    *productruntime.Runtime
}

type fixedEgressReader struct {
	page egressaudit.Page
}

func (reader fixedEgressReader) List(
	context.Context,
	egressaudit.PageRequest,
) (egressaudit.Page, error) {
	return reader.page, nil
}

type fixedConnectionReader struct {
	page connectionevent.Page
}

func (reader fixedConnectionReader) List(
	context.Context,
	connectionevent.PageRequest,
) (connectionevent.Page, error) {
	return reader.page, nil
}

func (reader fixedConnectionReader) Timeline(
	context.Context,
	string,
) (connectionevent.Timeline, error) {
	return connectionevent.Timeline{}, nil
}

func sampleAttempt(t *testing.T) egressaudit.Record {
	t.Helper()

	started := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	attempt, err := egressaudit.New(egressaudit.NewInput{
		ID:           "egress-sample",
		ConnectionID: "connection-sample",
		Purpose:      egressaudit.PurposeBlindTunnel,
		PayloadClass: egressaudit.PayloadOpaqueTunnel,
		Parent: egressaudit.ParentRef{
			Kind: egressaudit.ParentBlindConnection,
			ID:   "connection-sample",
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: "https://files.example.com:443",
		Decision: egressaudit.BuiltInDirectDecision(
			egressaudit.AuthorityNetwork,
		),
		StartedAt: started,
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := attempt.Finish(egressaudit.TerminalInput{
		Outcome:     egressaudit.OutcomeCompleted,
		BytesOut:    2048,
		BytesIn:     16384,
		CompletedAt: started.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	return egressaudit.Record{Sequence: 1, Attempt: finished}
}

func newAuditFixture(
	t *testing.T,
	captureReaders ...capturerun.Reader,
) *auditFixture {
	t.Helper()

	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	readToken := capability(0x41)
	writeToken := capability(0x42)
	authenticator, err := desktopcontrol.NewAuthenticator(
		desktopcontrol.CapabilityGrant{
			ReadToken:  readToken,
			WriteToken: writeToken,
			ExpiresAt:  now.Add(time.Hour),
		},
		fixedClock{now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	captureReader := runtime.CaptureRunReader()
	if len(captureReaders) > 0 {
		captureReader = captureReaders[0]
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness:     readyState(true),
		Status:        runtime,
		Accesses:      runtime.AccessWriter(),
		AccessCatalog: runtime.AccessCatalog(),
		Resolver:      runtime.SnapshotResolver(),
		Credentials:   runtime.Credentials(),
		Activities:    runtime.Activities(),
		Connections: fixedConnectionReader{
			page: connectionevent.Page{Items: []connectionevent.Record{
				sampleConnection(),
			}},
		},
		Egress: fixedEgressReader{
			page: egressaudit.Page{Items: []egressaudit.Record{
				sampleAttempt(t),
			}},
		},
		Approvals:       runtime.ToolApprovals(),
		CaptureRuns:     captureReader,
		Offline:         runtime,
		ConnectionRules: runtime.ConnectionRules(),
		WorkspaceRoutes: runtime.WorkspaceRoutes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	const authority = "127.0.0.1:43137"
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{
		Authority:      authority,
		AllowedOrigins: []string{"tauri://localhost"},
		Authenticator:  authenticator,
		Application:    application,
		Bootstrap:      emptyBootstrap(),
		CaptureRuns: http.HandlerFunc(func(
			http.ResponseWriter,
			*http.Request,
		) {
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &auditFixture{
		router:     router,
		authority:  authority,
		readToken:  readToken,
		writeToken: writeToken,
		runtime:    runtime,
	}
}

func sampleConnection() connectionevent.Record {
	started := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	return connectionevent.Record{
		Sequence: 1,
		Event: connectionevent.Event{
			ConnectionID:     "connection-sample",
			IngressID:        "run-sample",
			SourceLabel:      "claude",
			SourceConfidence: connectionevent.SourceConfidenceVerified,
			RequestedHost:    "files.example.com",
			Port:             443,
			Decision:         connectionevent.DecisionAllow,
			RuleID:           "allow.files",
			Decryption:       connectionevent.DecryptionBlind,
			Phase:            connectionevent.PhaseClosed,
			BytesUp:          2048,
			BytesDown:        16384,
			StartedAt:        started,
			EndedAt:          started.Add(3 * time.Second),
			Outcome:          connectionevent.OutcomeCompleted,
		},
	}
}
