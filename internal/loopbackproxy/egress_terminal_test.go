package loopbackproxy

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

func TestBlindTunnelDoesNotDialBeforeAuditAppend(t *testing.T) {
	t.Parallel()

	connections := &blindConnectionRepository{}
	manager, err := connectionevent.New(
		context.Background(),
		connectionevent.Options{
			Repository: connections,
			Clock:      connectionevent.SystemClock{},
			Random:     bytes.NewReader(bytes.Repeat([]byte{0x41}, 64)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := manager.Start(
		context.Background(),
		connectionevent.Attempt{
			Source: connectionevent.Source{
				Confidence: connectionevent.SourceConfidenceUnknown,
			},
			RequestedHost: "target.example",
			Port:          443,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	tunnels := &blindOrderingDialer{}
	egress := &failingBlindAppendWriter{}
	handler := &Handler{
		blindTunnels: tunnels,
		egressAudit:  egress,
		exchangeIDs:  fixedBlindExchangeIDSource{},
		ownerContext: context.Background(),
		clock:        time.Now,
	}
	request := httptest.NewRequest(
		http.MethodConnect,
		"http://target.example:443",
		nil,
	)
	writer := httptest.NewRecorder()
	terminal := handler.serveBlindTunnel(
		writer,
		request,
		&operation{},
		audit,
		connectionevent.Source{
			IngressID:  "run-ordering",
			Label:      "fixture",
			Confidence: connectionevent.SourceConfidenceConfigured,
		},
		"allow-target",
		"target.example:443",
		"target.example",
		443,
	)
	if !terminal {
		t.Fatal("blind path did not own its ConnectionEvent terminal")
	}
	if egress.appendCalls != 1 || tunnels.dialCalls != 0 {
		t.Fatalf(
			"audit appends=%d dial calls=%d",
			egress.appendCalls,
			tunnels.dialCalls,
		)
	}
	if writer.Code != http.StatusServiceUnavailable {
		t.Fatalf("response status = %d", writer.Code)
	}
	records := connections.snapshot()
	last := records[len(records)-1]
	if last.Outcome != connectionevent.OutcomeFailed ||
		last.ErrorClass != blindTunnelFailureClass {
		t.Fatalf("connection terminal = %+v", last)
	}
}

func TestBlindTunnelTerminalKeepsAuditOutcomesConsistent(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		err        error
		egress     egressaudit.Outcome
		connection connectionevent.Outcome
		errorClass string
	}{
		{
			name:       "completed",
			egress:     egressaudit.OutcomeCompleted,
			connection: connectionevent.OutcomeCompleted,
		},
		{
			name:       "canceled",
			err:        context.Canceled,
			egress:     egressaudit.OutcomeCanceled,
			connection: connectionevent.OutcomeCanceled,
			errorClass: "canceled",
		},
		{
			name:       "deadline",
			err:        context.DeadlineExceeded,
			egress:     egressaudit.OutcomeFailed,
			connection: connectionevent.OutcomeFailed,
			errorClass: "deadline",
		},
		{
			name:       "copy failure",
			err:        errors.New("copy failed"),
			egress:     egressaudit.OutcomeFailed,
			connection: connectionevent.OutcomeFailed,
			errorClass: blindTunnelFailureClass,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			egress, connection, errorClass := blindTunnelTerminal(test.err)
			if egress != test.egress ||
				connection != test.connection ||
				errorClass != test.errorClass {
				t.Fatalf(
					"terminal = (%q, %q, %q)",
					egress,
					connection,
					errorClass,
				)
			}
		})
	}
}

func TestBlindTunnelAuditClampsDecreasingWallClock(t *testing.T) {
	t.Parallel()

	startedAt := time.Now().UTC()
	clockCalls := 0
	audit := &blindTerminalAuditDouble{}
	handler := &Handler{
		egressAudit:  audit,
		ownerContext: context.Background(),
		clock: func() time.Time {
			clockCalls++
			if clockCalls == 1 {
				return startedAt
			}
			return startedAt.Add(-time.Minute)
		},
	}
	attempt, err := handler.beginBlindAudit(
		context.Background(),
		"blind-terminal-attempt",
		"blind-terminal-connection",
		"target.example:443",
	)
	if err != nil {
		t.Fatal(err)
	}
	handler.completeBlindAudit(
		attempt,
		egressaudit.OutcomeCompleted,
		"",
		blindtunnel.Result{BytesOut: 3, BytesIn: 5},
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

func TestBlindTunnelAuditReportsTerminalConstructionFailure(t *testing.T) {
	t.Parallel()

	startedAt := time.Now().UTC()
	audit := &blindTerminalAuditDouble{}
	handler := &Handler{
		egressAudit:  audit,
		ownerContext: context.Background(),
		clock:        func() time.Time { return time.Time{} },
	}
	handler.completeBlindAudit(
		blindTerminalAttempt(t, startedAt),
		egressaudit.OutcomeCompleted,
		"",
		blindtunnel.Result{BytesOut: 1},
	)

	_, calls, failures := audit.snapshot()
	if calls != 0 || len(failures) != 1 {
		t.Fatalf("completion calls=%d failures=%v", calls, failures)
	}
	if !strings.Contains(failures[0].Error(), "blind-tunnel EgressAttempt terminal") {
		t.Fatalf("reported failure = %v", failures[0])
	}
}

func TestBlindTunnelAuditBoundsNonProductionWriterCompletion(t *testing.T) {
	t.Parallel()

	startedAt := time.Now().UTC().Add(-time.Second)
	audit := &blockingBlindAuditWriter{result: make(chan error, 1)}
	handler := &Handler{
		egressAudit:  audit,
		ownerContext: context.Background(),
		clock:        time.Now,
		auditTimeout: 25 * time.Millisecond,
	}

	started := time.Now()
	handler.completeBlindAudit(
		blindTerminalAttempt(t, startedAt),
		egressaudit.OutcomeCompleted,
		"",
		blindtunnel.Result{BytesOut: 1},
	)
	elapsed := time.Since(started)
	if elapsed < 10*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("bounded audit completion elapsed = %s", elapsed)
	}
	select {
	case err := <-audit.result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("audit completion context error = %v", err)
		}
	default:
		t.Fatal("audit writer did not observe its completion deadline")
	}
}

func blindTerminalAttempt(t *testing.T, startedAt time.Time) egressaudit.Attempt {
	t.Helper()
	attempt, err := egressaudit.New(egressaudit.NewInput{
		ID:           "blind-terminal-attempt",
		ConnectionID: "blind-terminal-connection",
		Purpose:      egressaudit.PurposeBlindTunnel,
		PayloadClass: egressaudit.PayloadOpaqueTunnel,
		Parent: egressaudit.ParentRef{
			Kind: egressaudit.ParentBlindConnection,
			ID:   "blind-terminal-connection",
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: "https://target.example:443",
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

type blindTerminalAuditDouble struct {
	mu       sync.Mutex
	terminal egressaudit.Attempt
	calls    int
	failures []error
}

func (*blindTerminalAuditDouble) Append(
	context.Context,
	egressaudit.Attempt,
) (egressaudit.Record, error) {
	return egressaudit.Record{}, nil
}

func (audit *blindTerminalAuditDouble) Complete(
	_ context.Context,
	attempt egressaudit.Attempt,
) (egressaudit.Record, error) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.calls++
	audit.terminal = attempt
	return egressaudit.Record{Attempt: attempt}, nil
}

func (audit *blindTerminalAuditDouble) ReportTerminalFailure(err error) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.failures = append(audit.failures, err)
}

func (audit *blindTerminalAuditDouble) snapshot() (
	egressaudit.Attempt,
	int,
	[]error,
) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	return audit.terminal, audit.calls, append([]error(nil), audit.failures...)
}

var _ egressaudit.Writer = (*blindTerminalAuditDouble)(nil)
var _ terminalFailureReporter = (*blindTerminalAuditDouble)(nil)

type blockingBlindAuditWriter struct {
	result chan error
}

func (*blockingBlindAuditWriter) Append(
	context.Context,
	egressaudit.Attempt,
) (egressaudit.Record, error) {
	return egressaudit.Record{}, nil
}

func (writer *blockingBlindAuditWriter) Complete(
	ctx context.Context,
	_ egressaudit.Attempt,
) (egressaudit.Record, error) {
	<-ctx.Done()
	writer.result <- ctx.Err()
	return egressaudit.Record{}, ctx.Err()
}

var _ egressaudit.Writer = (*blockingBlindAuditWriter)(nil)

type fixedBlindExchangeIDSource struct{}

func (fixedBlindExchangeIDSource) NewExchangeID(
	context.Context,
) (string, error) {
	return "blind-ordering-attempt", nil
}

type blindOrderingDialer struct {
	dialCalls int
}

func (*blindOrderingDialer) BeginAction(
	context.Context,
	offlinehold.ActionRequest,
) (*offlinehold.ActionLease, error) {
	return &offlinehold.ActionLease{}, nil
}

func (dialer *blindOrderingDialer) Dial(
	context.Context,
	blindtunnel.DialRequest,
) (net.Conn, offlinehold.Lease, error) {
	dialer.dialCalls++
	return nil, nil, errors.New("dial must not be reached")
}

type failingBlindAppendWriter struct {
	appendCalls int
}

func (writer *failingBlindAppendWriter) Append(
	context.Context,
	egressaudit.Attempt,
) (egressaudit.Record, error) {
	writer.appendCalls++
	return egressaudit.Record{}, errors.New("audit unavailable")
}

func (*failingBlindAppendWriter) Complete(
	context.Context,
	egressaudit.Attempt,
) (egressaudit.Record, error) {
	return egressaudit.Record{}, errors.New("unexpected completion")
}

type blindConnectionRepository struct {
	mu      sync.Mutex
	records []connectionevent.Record
}

func (repository *blindConnectionRepository) Append(
	_ context.Context,
	event connectionevent.Event,
) (connectionevent.Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	record := connectionevent.Record{
		Sequence: int64(len(repository.records) + 1),
		Event:    event,
	}
	if err := record.Validate(); err != nil {
		return connectionevent.Record{}, err
	}
	repository.records = append(repository.records, record)
	return record, nil
}

func (repository *blindConnectionRepository) List(
	context.Context,
	connectionevent.PageRequest,
) (connectionevent.Page, error) {
	return connectionevent.Page{Items: repository.snapshot()}, nil
}

func (repository *blindConnectionRepository) Timeline(
	_ context.Context,
	connectionID string,
) (connectionevent.Timeline, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	timeline := connectionevent.Timeline{ConnectionID: connectionID}
	for _, record := range repository.records {
		if record.ConnectionID == connectionID {
			timeline.Events = append(timeline.Events, record)
		}
	}
	return timeline, nil
}

func (*blindConnectionRepository) Recover(
	context.Context,
	time.Time,
) (int, error) {
	return 0, nil
}

func (repository *blindConnectionRepository) snapshot() []connectionevent.Record {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]connectionevent.Record(nil), repository.records...)
}
