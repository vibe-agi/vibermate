package exchange

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

// Concrete bounded DATA-emitting observation boundary, not a fake keepalive
// counter. Production httpDownstream encoding/accounting has separate tests.
type budgetDataDownstream struct {
	downstreamRecorder
	liveness bytes.Buffer
	frames   []rawevidence.Frame
}

func (d *budgetDataDownstream) Keepalive(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wire, err := openairesponses.EncodeKeepalive()
	if err != nil {
		return err
	}
	if d.liveness.Len()+len(wire) > 16<<10 {
		return errors.New("bounded liveness observation overflow")
	}
	offset := d.liveness.Len()
	n, err := d.liveness.Write(wire)
	if n > 0 {
		d.frames = append(d.frames, rawevidence.Frame{Kind: rawevidence.FrameKeepalive, Offset: int64(offset), Length: int64(n)})
	}
	return err
}

type idleBudgetBody struct {
	*io.PipeReader
	closed chan struct{}
	once   sync.Once
	active atomic.Int32
}

func (b *idleBudgetBody) Read(p []byte) (int, error) {
	b.active.Add(1)
	defer b.active.Add(-1)
	return b.PipeReader.Read(p)
}
func (b *idleBudgetBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return b.PipeReader.Close()
}

func TestLocalResponsesDataKeepaliveDoesNotRenewProviderSemanticBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	body := &idleBudgetBody{PipeReader: reader, closed: make(chan struct{})}
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer writer.Close()
		_, _ = io.WriteString(writer, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_budget\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		<-body.closed
	}()
	defer body.Close()
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{clientProtocol: environment.ClientProtocolOpenAIResponses, destination: environment.DestinationKindUpstream, providerOrigin: "https://provider.example", backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough, accounts: []testAccount{{id: "account.budget", revision: 1, epoch: 1}}, preferred: "account.budget"})
	provider := &providerDouble{results: []providerResult{{response: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}}}}
	decisions := approvedDecisions()
	observer := &attemptObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(t, newAccountAuthority(t, testAccount{id: "account.budget", revision: 1, epoch: 1}), provider, decisions, observer, &contentObserverDouble{}, StreamBudgets{KeepaliveInterval: 10 * time.Millisecond, ProviderProgressTimeout: 100 * time.Millisecond})
	defer shutdownPipeline(t, pipeline)
	downstream := &budgetDataDownstream{}
	started := time.Now()
	result, err := pipeline.Execute(ctx, mustClientRequest(t, "exchange-local-liveness-budget", plan, streamingResponsesClientRequest()), downstream)
	elapsed := time.Since(started)
	if !errors.Is(err, ErrProviderSemanticIdle) || ctx.Err() != nil || elapsed < 80*time.Millisecond || elapsed > time.Second || result.Outcome == AttemptSucceeded {
		t.Fatalf("real semantic budget differs: result=%s err=%v elapsed=%v", result.Outcome, err, elapsed)
	}
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("provider body producer not drained")
	}
	if body.active.Load() != 0 {
		t.Fatal("provider read pump survived Execute")
	}
	decoder, _ := ssewire.NewDecoder(ssewire.DefaultOptions())
	events, decodeErr := decoder.Feed(downstream.liveness.Bytes())
	if decodeErr != nil || len(events) < 3 || len(events) != len(downstream.frames) {
		t.Fatal("multiple actual DATA keepalive writes not observed")
	}
	for i, event := range events {
		if event.Name != "keepalive" || string(event.Data) != `{"type":"keepalive"}` || downstream.frames[i].Kind != rawevidence.FrameKeepalive {
			t.Fatal("liveness carried model content")
		}
	}
	if bytes.Contains(downstream.bytesSnapshot(), []byte("function_call")) || bytes.Contains(downstream.bytesSnapshot(), []byte("response.completed")) || decisions.callCount() != 0 || len(observer.snapshot()) != 1 {
		t.Fatal("silent provider created tool/success/retry effects")
	}
	t.Logf("semantic idle at=%dms concrete DATA keepalives=%d readPumpActive=%d outcome=%s", elapsed.Milliseconds(), len(events), body.active.Load(), result.Outcome)
}
