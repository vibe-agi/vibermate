package providertransport

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
)

func TestProviderAuditRecordsResponseReadFailure(t *testing.T) {
	t.Parallel()

	readFailure := errors.New("provider response read failed")
	audit := &providerTerminalAuditDouble{}
	gate := newStartedGate(t)
	authenticator, err := NewStaticBearerAuthenticator(
		&secretReaderStub{value: []byte("provider-token")},
	)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		Audit:         audit,
		Transport: &roundTripperStub{response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &failingProviderBody{err: readFailure},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)
	response, _, err := client.Do(
		context.Background(),
		newTestRequest(
			t,
			gate,
			"response-read-failure",
			testTarget("provider.example", 443),
			nil,
		),
	)
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
}

func TestProviderResponseAuditTerminalClassifiesCancellationAndTimeout(
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

func TestProviderAuditClampsDecreasingWallClock(t *testing.T) {
	t.Parallel()

	startedAt := time.Now().UTC()
	audit := &providerTerminalAuditDouble{}
	client := &Client{
		audit: audit,
		clock: func() time.Time { return startedAt.Add(-time.Minute) },
	}
	client.completeAudit(
		context.Background(),
		providerTerminalAttempt(t, startedAt),
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

func TestProviderAuditReportsTerminalConstructionFailure(t *testing.T) {
	t.Parallel()

	startedAt := time.Now().UTC()
	audit := &providerTerminalAuditDouble{}
	client := &Client{audit: audit, clock: func() time.Time { return time.Time{} }}
	client.completeAudit(
		context.Background(),
		providerTerminalAttempt(t, startedAt),
		egressaudit.OutcomeCompleted,
		"",
		1,
		0,
	)

	_, calls, failures := audit.snapshot()
	if calls != 0 || len(failures) != 1 {
		t.Fatalf("completion calls=%d failures=%v", calls, failures)
	}
	if !strings.Contains(failures[0].Error(), "provider EgressAttempt terminal") {
		t.Fatalf("reported failure = %v", failures[0])
	}
}

func providerTerminalAttempt(t *testing.T, startedAt time.Time) egressaudit.Attempt {
	t.Helper()
	attempt, err := egressaudit.New(egressaudit.NewInput{
		ID:           "provider-terminal-attempt",
		ConnectionID: "provider-terminal-connection",
		Purpose:      egressaudit.PurposeProviderAttempt,
		PayloadClass: egressaudit.PayloadClientSemantic,
		Parent: egressaudit.ParentRef{
			Kind:       egressaudit.ParentUpstreamAttempt,
			ID:         "provider-terminal-upstream",
			ExchangeID: "provider-terminal-exchange",
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: "https://provider.example:443",
		Decision:     providerEgressDecision(),
		StartedAt:    startedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

type providerTerminalAuditDouble struct {
	mu       sync.Mutex
	terminal egressaudit.Attempt
	calls    int
	failures []error
}

func (*providerTerminalAuditDouble) Append(
	context.Context,
	egressaudit.Attempt,
) (egressaudit.Record, error) {
	return egressaudit.Record{}, nil
}

func (audit *providerTerminalAuditDouble) Complete(
	_ context.Context,
	attempt egressaudit.Attempt,
) (egressaudit.Record, error) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.calls++
	audit.terminal = attempt
	return egressaudit.Record{Attempt: attempt}, nil
}

func (audit *providerTerminalAuditDouble) ReportTerminalFailure(err error) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.failures = append(audit.failures, err)
}

func (audit *providerTerminalAuditDouble) snapshot() (
	egressaudit.Attempt,
	int,
	[]error,
) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	return audit.terminal, audit.calls, append([]error(nil), audit.failures...)
}

var _ egressaudit.Writer = (*providerTerminalAuditDouble)(nil)
var _ terminalFailureReporter = (*providerTerminalAuditDouble)(nil)

type failingProviderBody struct {
	err error
}

func (body *failingProviderBody) Read([]byte) (int, error) {
	return 0, body.err
}

func (*failingProviderBody) Close() error {
	return nil
}
