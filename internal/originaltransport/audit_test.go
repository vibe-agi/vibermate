package originaltransport_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

type auditRecorder struct {
	mu        sync.Mutex
	appended  []egressaudit.Attempt
	completed []egressaudit.Attempt
}

func (recorder *auditRecorder) Append(
	_ context.Context,
	attempt egressaudit.Attempt,
) (egressaudit.Record, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.appended = append(recorder.appended, attempt)
	return egressaudit.Record{Attempt: attempt}, nil
}

func (recorder *auditRecorder) Complete(
	_ context.Context,
	attempt egressaudit.Attempt,
) (egressaudit.Record, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.completed = append(recorder.completed, attempt)
	return egressaudit.Record{Attempt: attempt}, nil
}

func (recorder *auditRecorder) snapshot() (
	[]egressaudit.Attempt,
	[]egressaudit.Attempt,
) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]egressaudit.Attempt(nil), recorder.appended...),
		append([]egressaudit.Attempt(nil), recorder.completed...)
}

func auditedRequest(t *testing.T) originaltransport.Request {
	t.Helper()

	origin, err := originidentity.ParseClientOrigin("https://api.anthropic.com:443")
	if err != nil {
		t.Fatal(err)
	}
	request, err := originaltransport.NewRequest(
		originaltransport.RequestOptions{
			RequestID:    "egress-original-1",
			Kind:         offlinehold.EgressOpaque,
			Origin:       origin,
			Method:       http.MethodGet,
			Path:         "/api/claude_code/settings",
			PayloadClass: protocolspec.OperationPayloadControl,
			ConnectionID: "connection-1",
			ParentID:     "original-request-1",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// Every real outbound leaves one immutable record naming where it went, so a
// persistent connection carrying several requests cannot hide an earlier
// destination behind a later one.
func TestOriginalEgressRecordsOneAttemptPerOutbound(t *testing.T) {
	t.Parallel()

	recorder := &auditRecorder{}
	client, err := originaltransport.New(originaltransport.Options{
		Coordinator: &recordingCoordinator{},
		Audit:       recorder,
		Transport: &roundTripper{response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("original")),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(context.Background(), auditedRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	appended, completed := recorder.snapshot()
	if len(appended) != 1 {
		t.Fatalf("appended attempts = %d, want 1", len(appended))
	}
	attempt := appended[0]
	if attempt.Purpose() != egressaudit.PurposeOriginalOrigin ||
		attempt.PayloadClass() != egressaudit.PayloadControl ||
		attempt.ConnectionID() != "connection-1" ||
		attempt.Parent().Kind != egressaudit.ParentOriginalRequest ||
		attempt.Parent().ID != "original-request-1" ||
		attempt.TargetOrigin() != originidentityMustString(t, "https://api.anthropic.com:443") {
		t.Fatalf("recorded attempt = %+v parent=%+v",
			attempt, attempt.Parent())
	}
	if len(completed) != 1 ||
		completed[0].Outcome() != egressaudit.OutcomeCompleted {
		t.Fatalf("completed attempts = %+v", completed)
	}
}

func originidentityMustString(t *testing.T, raw string) string {
	t.Helper()
	origin, err := originidentity.ParseClientOrigin(raw)
	if err != nil {
		t.Fatal(err)
	}
	return origin.String()
}

// A failed outbound is still evidence; it must not vanish.
func TestOriginalEgressRecordsAFailedOutbound(t *testing.T) {
	t.Parallel()

	recorder := &auditRecorder{}
	client, err := originaltransport.New(originaltransport.Options{
		Coordinator: &recordingCoordinator{},
		Audit:       recorder,
		Transport:   &failingRoundTripper{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(
		context.Background(),
		auditedRequest(t),
	); err == nil {
		t.Fatal("a failing transport returned no error")
	}
	appended, completed := recorder.snapshot()
	if len(appended) != 1 {
		t.Fatalf("appended attempts = %d", len(appended))
	}
	if len(completed) != 1 ||
		completed[0].Outcome() != egressaudit.OutcomeFailed {
		t.Fatalf("completed attempts = %+v", completed)
	}
}

type failingRoundTripper struct{}

func (transport *failingRoundTripper) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	if request.Body != nil {
		_, _ = io.Copy(io.Discard, request.Body)
	}
	return nil, errors.New("transport failed")
}
