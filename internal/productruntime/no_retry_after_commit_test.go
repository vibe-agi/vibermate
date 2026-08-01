package productruntime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

// failingAfterBytesProvider answers, streams a little, and then fails. It is
// the shape of an upstream that dies partway through an answer.
type failingAfterBytesProvider struct {
	mu    sync.Mutex
	calls int
}

func (provider *failingAfterBytesProvider) Do(
	context.Context,
	providertransport.Request,
) (*http.Response, providertransport.Evidence, error) {
	provider.mu.Lock()
	provider.calls++
	provider.mu.Unlock()
	// The backend speaks OpenAI chat completions, so the truncated answer has
	// to be in that grammar for the pipeline to commit anything downstream.
	events := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk",` +
		`"created":1,"model":"gpt-4.1-mini","choices":[{"index":0,` +
		`"delta":{"role":"assistant","content":""},"finish_reason":null}]}` +
		"\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk",` +
		`"created":1,"model":"gpt-4.1-mini","choices":[{"index":0,` +
		`"delta":{"content":"partial"},"finish_reason":null}]}` +
		"\n\n"
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &truncatedBody{reader: strings.NewReader(events)},
	}, providertransport.Evidence{}, nil
}

func (provider *failingAfterBytesProvider) Shutdown(context.Context) error {
	return nil
}

func (provider *failingAfterBytesProvider) attempts() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

// truncatedBody delivers its bytes and then fails, the way a connection that
// drops mid-answer does.
type truncatedBody struct {
	reader *strings.Reader
}

func (body *truncatedBody) Read(buffer []byte) (int, error) {
	read, err := body.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		return read, errors.New("upstream connection dropped")
	}
	return read, err
}

func (*truncatedBody) Close() error { return nil }

// Once bytes have reached the client, the answer is committed and there is no
// second attempt: a retry would send a second beginning to a client already
// reading the first.
//
// The pipeline has no attempt loop today, so this holds by construction. The
// test exists because that is not a reason to stop being true — the day a
// RouteSet's candidates are actually tried, this is the rule that has to
// survive it, and design 02 §12 states it as `pre_first_byte_idempotent_only`.
func TestAStreamThatFailedAfterCommitIsNotRetried(t *testing.T) {
	t.Parallel()

	accessID, err := access.NewAccessID("access-no-retry")
	if err != nil {
		t.Fatal(err)
	}
	provider := &failingAfterBytesProvider{}
	builders := productionBuilders()
	builders.provider = fixedProviderBuilder{component: provider}
	runtime, err := startWithBuilders(
		context.Background(),
		testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}),
		builders,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntime(t, runtime)
	if write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate:        runtimeAccessAggregate(t, accessID, 1, "No Retry"),
		},
	); err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	activePlan, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewClientRequest(
		"exchange-no-retry",
		activePlan.IngressBinding(),
		runtimeAnthropicOperationEvidence(t),
		[]byte(`{
			"model":"claude-client-alias",
			"max_tokens":64,
			"stream":true,
			"messages":[{"role":"user","content":"hello"}]
		}`),
		exchange.ReplayGenerationCostOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	downstream := &runtimeDownstream{}
	result, err := runtime.ExchangeExecutor().Execute(
		context.Background(),
		request,
		downstream,
	)
	if err == nil {
		t.Fatal("a truncated upstream answer reported success")
	}
	if downstream.body.Len() == 0 {
		t.Fatal("nothing was committed, so nothing was at risk of being resent")
	}
	if result.Ledger.DownstreamTerminal {
		t.Fatalf("a truncated answer was reported as complete: %+v", result.Ledger)
	}
	if provider.attempts() != 1 {
		t.Fatalf(
			"a committed answer was attempted %d times",
			provider.attempts(),
		)
	}

	// The outbound record is not asserted here: this test substitutes the
	// provider transport, which is where the audit is written. The live runs
	// cover that an interrupted outbound reaches a terminal.
}
