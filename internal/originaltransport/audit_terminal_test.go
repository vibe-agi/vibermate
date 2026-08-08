package originaltransport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestOriginalAuditRecordsResponseReadFailure(t *testing.T) {
	t.Parallel()

	readFailure := errors.New("original response read failed")
	audit := &originalTerminalAuditDouble{}
	client, err := New(Options{
		Coordinator: originalAuditCoordinator{},
		Audit:       audit,
		Transport: originalAuditRoundTripper{response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &failingOriginalBody{err: readFailure},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := originalAuditRequest(t)
	response, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := response.Body.Read(make([]byte, 1)); !errors.Is(err, readFailure) {
		t.Fatalf("response Read() error = %v", err)
	}
	terminal, calls, failures := audit.snapshot()
	if calls != 1 || len(failures) != 0 {
		t.Fatalf("completion calls=%d failures=%v", calls, failures)
	}
	if terminal.Outcome() != egressaudit.OutcomeFailed ||
		terminal.ErrorClass() != responseFailureClass {
		t.Fatalf("terminal attempt = %+v", terminal)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestOriginalResponseAuditTerminalClassifiesCancellationAndTimeout(
	t *testing.T,
) {
	t.Parallel()

	canceled, cancel := context.WithCancelCause(context.Background())
	cancel(ErrClientClosing)
	outcome, class := responseAuditTerminal(canceled, nil)
	if outcome != egressaudit.OutcomeCanceled || class != responseCanceledClass {
		t.Fatalf("canceled terminal = (%q, %q)", outcome, class)
	}
	deadline, cancelDeadline := context.WithDeadline(
		context.Background(),
		time.Now().Add(-time.Second),
	)
	defer cancelDeadline()
	outcome, class = responseAuditTerminal(deadline, context.DeadlineExceeded)
	if outcome != egressaudit.OutcomeFailed || class != responseTimeoutClass {
		t.Fatalf("deadline terminal = (%q, %q)", outcome, class)
	}
	outcome, class = responseAuditTerminal(context.Background(), io.EOF)
	if outcome != egressaudit.OutcomeCompleted || class != "" {
		t.Fatalf("EOF terminal = (%q, %q)", outcome, class)
	}
}

func TestOriginalAuditClampsDecreasingWallClock(t *testing.T) {
	t.Parallel()

	startedAt := time.Now().UTC()
	audit := &originalTerminalAuditDouble{}
	client := &Client{
		audit: audit,
		clock: func() time.Time { return startedAt.Add(-time.Minute) },
	}
	client.completeAudit(
		context.Background(),
		originalTerminalAttempt(t, startedAt),
		egressaudit.OutcomeCompleted,
		"",
		3,
		5,
	)

	terminal, calls, failures := audit.snapshot()
	if calls != 1 || len(failures) != 0 {
		t.Fatalf("completion calls=%d failures=%v", calls, failures)
	}
	if !terminal.CompletedAt().Equal(startedAt) {
		t.Fatalf(
			"completion time = %s, want clamped %s",
			terminal.CompletedAt(),
			startedAt,
		)
	}
}

func TestOriginalAuditReportsTerminalConstructionFailure(t *testing.T) {
	t.Parallel()

	startedAt := time.Now().UTC()
	audit := &originalTerminalAuditDouble{}
	client := &Client{audit: audit, clock: func() time.Time { return time.Time{} }}
	client.completeAudit(
		context.Background(),
		originalTerminalAttempt(t, startedAt),
		egressaudit.OutcomeCompleted,
		"",
		1,
		0,
	)

	_, calls, failures := audit.snapshot()
	if calls != 0 || len(failures) != 1 {
		t.Fatalf("completion calls=%d failures=%v", calls, failures)
	}
	if !strings.Contains(failures[0].Error(), "original-origin EgressAttempt terminal") {
		t.Fatalf("reported failure = %v", failures[0])
	}
}

func originalTerminalAttempt(t *testing.T, startedAt time.Time) egressaudit.Attempt {
	t.Helper()
	attempt, err := egressaudit.New(egressaudit.NewInput{
		ID:           "original-terminal-attempt",
		ConnectionID: "original-terminal-connection",
		Purpose:      egressaudit.PurposeOriginalOrigin,
		PayloadClass: egressaudit.PayloadControl,
		Parent: egressaudit.ParentRef{
			Kind: egressaudit.ParentOriginalRequest,
			ID:   "original-terminal-request",
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: "https://origin.example:443",
		Decision: egressaudit.BuiltInDirectDecision(
			egressaudit.AuthorityNetwork,
		),
		StartedAt: startedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

type originalTerminalAuditDouble struct {
	mu       sync.Mutex
	terminal egressaudit.Attempt
	calls    int
	failures []error
}

func (*originalTerminalAuditDouble) Append(
	context.Context,
	egressaudit.Attempt,
) (egressaudit.Record, error) {
	return egressaudit.Record{}, nil
}

func (audit *originalTerminalAuditDouble) Complete(
	_ context.Context,
	attempt egressaudit.Attempt,
) (egressaudit.Record, error) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.calls++
	audit.terminal = attempt
	return egressaudit.Record{Attempt: attempt}, nil
}

func (audit *originalTerminalAuditDouble) ReportTerminalFailure(err error) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.failures = append(audit.failures, err)
}

func (audit *originalTerminalAuditDouble) snapshot() (
	egressaudit.Attempt,
	int,
	[]error,
) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	return audit.terminal, audit.calls, append([]error(nil), audit.failures...)
}

var _ egressaudit.Writer = (*originalTerminalAuditDouble)(nil)
var _ terminalFailureReporter = (*originalTerminalAuditDouble)(nil)

func originalAuditRequest(t *testing.T) Request {
	t.Helper()
	origin, err := originidentity.ParseClientOrigin("https://origin.example:443")
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewRequest(RequestOptions{
		RequestID:    "original-read-failure",
		Kind:         offlinehold.EgressOpaque,
		Origin:       origin,
		Method:       http.MethodGet,
		Path:         "/status",
		PayloadClass: protocolspec.OperationPayloadControl,
		ConnectionID: "connection-read-failure",
		ParentID:     "request-read-failure",
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

type originalAuditCoordinator struct{}

func (originalAuditCoordinator) Start(
	context.Context,
	offlinehold.RuntimeBinding,
) error {
	return nil
}

func (originalAuditCoordinator) BeginAction(
	context.Context,
	offlinehold.ActionRequest,
) (*offlinehold.ActionLease, error) {
	return &offlinehold.ActionLease{}, nil
}

func (originalAuditCoordinator) Acquire(
	context.Context,
	offlinehold.AcquireRequest,
) (offlinehold.Lease, error) {
	return originalAuditLease{}, nil
}

func (originalAuditCoordinator) BeginShutdown() {}

func (originalAuditCoordinator) Drain(context.Context) error { return nil }

func (originalAuditCoordinator) Snapshot() offlinehold.Snapshot {
	return offlinehold.Snapshot{State: offlinehold.StateOnline}
}

type originalAuditLease struct{}

func (originalAuditLease) Release() {}

type originalAuditRoundTripper struct {
	response *http.Response
}

func (transport originalAuditRoundTripper) RoundTrip(
	*http.Request,
) (*http.Response, error) {
	return transport.response, nil
}

type failingOriginalBody struct {
	err error
}

func (body *failingOriginalBody) Read([]byte) (int, error) {
	return 0, body.err
}

func (*failingOriginalBody) Close() error {
	return nil
}
