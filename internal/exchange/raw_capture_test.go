package exchange

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type saturatedRawRepository struct {
	rawevidence.Repository
	entered, release chan struct{}
	once             sync.Once
}

func (r *saturatedRawRepository) RedactionSalt(context.Context) ([]byte, error) {
	return bytes.Repeat([]byte{1}, 32), nil
}
func (r *saturatedRawRepository) BeginWriterSession(context.Context, rawevidence.WriterSession) (rawevidence.Recovery, error) {
	return rawevidence.Recovery{}, nil
}
func (r *saturatedRawRepository) CloseWriterSession(context.Context, string, time.Time) error {
	return nil
}
func (r *saturatedRawRepository) AppendBatch(ctx context.Context, _ []rawevidence.StoredEnvelope, _ time.Time) error {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestTransformStreamCaptureOwnsInputAndDisabledPreservesForwarding(t *testing.T) {
	program, err := messagetransform.CompilePipeline([]messagetransform.Policy{{ResponseJavaScript: `response.headers["x-transform"] = "yes";`}}, messagetransform.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var outputs [2][]byte
	for i := range 2 {
		sink := &transformRawRepository{}
		budget, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 1 << 20})
		manager, err := rawevidence.Open(context.Background(), rawevidence.Options{Budget: budget, Repository: sink, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)), Clock: rawevidence.SystemClock{}, Config: rawevidence.DefaultConfig()})
		if err != nil {
			t.Fatal(err)
		}
		transformer, err := newStreamMessageTransformer(program.NewTurn(), 200, http.Header{"Content-Type": {"text/event-stream"}})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			transformer.capture = manager.Capture(rawevidence.CaptureMetadata{Context: rawevidence.Context{ScopeKind: rawevidence.ScopeRuntime, ExchangeID: "transform-owned", Recording: rawevidence.RecordingFull, RetentionDays: 1}, Layer: rawevidence.LayerTransformResponseInput, StatusCode: 200, Headers: transformer.headers}, 16<<20)
		}
		fragment := []byte("data: {\"value\":1}\n\n")
		var expected []byte
		for range 40000 {
			wire, _, err := transformer.Feed(context.Background(), fragment)
			if err != nil {
				t.Fatal(err)
			}
			outputs[i] = append(outputs[i], wire...)
			expected = append(expected, fragment...)
		}
		if err := transformer.Finish(); err != nil {
			t.Fatal(err)
		}
		if transformer.afterHeaders.Get("X-Transform") != "yes" {
			t.Fatal("transformed headers changed")
		}
		if i == 0 {
			if transformer.capture.Finish(rawevidence.TerminalMetadata{Complete: true}) != recording.Accepted {
				t.Fatal("capture refused")
			}
			if err := manager.Flush(context.Background(), rawevidence.Watermark{}); err != nil {
				t.Fatal(err)
			}
			if len(sink.records) != 1 || len(sink.records[0].Body) == 0 || !bytes.Equal(sink.records[0].Body, expected[:len(sink.records[0].Body)]) || sink.records[0].BodyBytes != int64(len(expected)) {
				t.Fatal("transform Raw history is unowned or inaccurate")
			}
		} else if budget.Snapshot().ReservedRecords != 0 {
			t.Fatal("disabled transform retained Raw")
		}
		manager.Shutdown(context.Background())
	}
	if !bytes.Equal(outputs[0], outputs[1]) {
		t.Fatal("recording changed transformed SSE")
	}
}

type transformRawRepository struct {
	saturatedRawRepository
	records []rawevidence.StoredEnvelope
}

func (r *transformRawRepository) AppendBatch(_ context.Context, records []rawevidence.StoredEnvelope, _ time.Time) error {
	r.records = append(r.records, records...)
	return nil
}

type rawCaptureObserverForTest struct {
	*rawevidence.Manager
	repo *transformRawRepository
	t    *testing.T
}

func newRawCaptureObserverForTest(t *testing.T) *rawCaptureObserverForTest {
	t.Helper()
	repo := &transformRawRepository{}
	m, err := rawevidence.Open(context.Background(), rawevidence.Options{Repository: repo, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)), Clock: rawevidence.SystemClock{}, Config: rawevidence.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return &rawCaptureObserverForTest{Manager: m, repo: repo, t: t}
}
func (o *rawCaptureObserverForTest) snapshot() []rawevidence.Observation {
	if err := o.Flush(context.Background(), rawevidence.Watermark{}); err != nil {
		o.t.Fatal(err)
	}
	var result []rawevidence.Observation
	for _, record := range o.repo.records {
		payload, err := o.ReadPayload(record)
		if err != nil {
			o.t.Fatal(err)
		}
		headers := make(http.Header)
		for _, field := range payload.Headers {
			headers[field.Name] = field.Values
		}
		result = append(result, rawevidence.Observation{Layer: record.Layer, Headers: headers, Body: payload.Body, Representation: record.Representation})
	}
	return result
}

type delayedTransformTailBody struct {
	prefix            *bytes.Reader
	tail              []byte
	allowTail, closed chan struct{}
	closeOnce         sync.Once
	original, eof     bool
	active            atomic.Int32
	tailRead          atomic.Bool
}

func (b *delayedTransformTailBody) Read(p []byte) (int, error) {
	b.active.Add(1)
	defer b.active.Add(-1)
	if b.prefix.Len() > 0 {
		n, _ := b.prefix.Read(p)
		if b.eof && b.prefix.Len() == 0 {
			return n, io.EOF
		}
		return n, nil
	}
	if b.original {
		return 0, context.Canceled
	}
	select {
	case <-b.closed:
		return 0, io.ErrClosedPipe
	case <-b.allowTail:
		b.tailRead.Store(true)
		return copy(p, b.tail), io.EOF
	}
}
func (b *delayedTransformTailBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestTransformRawDigestRequiresLogicalEOFNotSemanticTerminal(t *testing.T) {
	for _, original := range []bool{false, true} {
		for _, eof := range []bool{false, true} {
			t.Run(fmt.Sprintf("original=%t/eof=%t", original, eof), func(t *testing.T) {
				account := testAccount{id: "account.primary", revision: 1, epoch: 1}
				options := testPlanOptions{clientProtocol: environment.ClientProtocolOpenAIResponses, destination: environment.DestinationKindUpstream, providerOrigin: "https://api.openai.com", backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough, accounts: []testAccount{account}, preferred: account.id, transform: messagetransform.Policy{ResponseJavaScript: `response.headers["x-transform"] = "yes";`}}
				if original {
					options.destination = environment.DestinationKindOriginal
					options.accounts = nil
					options.preferred = ""
				}
				plan := mustEnvironmentRequestPlan(t, options)
				wire := originalResponsesTerminalWire(t)
				body := &delayedTransformTailBody{prefix: bytes.NewReader(wire), tail: []byte(": delayed tail\n\n"), allowTail: make(chan struct{}), closed: make(chan struct{}), original: original, eof: eof}
				defer close(body.allowTail)
				defer body.Close()
				response := streamResponse(http.StatusOK, body)
				response.Body = body
				pipeline := newTestPipeline(t, newAccountAuthority(t, account), &providerDouble{results: []providerResult{{response: response}}}, approvedDecisions(), &attemptObserverDouble{})
				defer shutdownPipeline(t, pipeline)
				raw := newRawCaptureObserverForTest(t)
				pipeline.rawEvidence = raw
				downstream := &downstreamRecorder{}
				request := mustClientRequestWithOptions(t, "terminal-tail", plan, streamingResponsesClientRequest(), WithOriginalHeaders(http.Header{"Authorization": {"Bearer fixture"}}))
				done := make(chan error, 1)
				joined := make(chan struct{})
				defer func() { body.Close(); <-joined }()
				go func() {
					defer close(joined)
					_, err := pipeline.Execute(context.Background(), request, downstream)
					done <- err
				}()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("semantic completion waited for unread tail")
				}
				if body.active.Load() != 0 || body.tailRead.Load() {
					t.Fatal("semantic completion lost I/O join or drained optional tail")
				}
				if !bytes.Equal(downstream.bytesSnapshot(), wire) {
					t.Fatal("transformed forwarded bytes changed")
				}
				if err := raw.Flush(context.Background(), rawevidence.Watermark{}); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, record := range raw.repo.records {
					if record.Layer != rawevidence.LayerTransformResponseInput {
						continue
					}
					found = true
					expectedDigest, expectedState := rawevidence.DigestObservedPrefix, rawevidence.PayloadTruncated
					if eof {
						expectedDigest = rawevidence.DigestFull
						expectedState = rawevidence.PayloadCaptured
					}
					if record.DigestScope != expectedDigest || record.PayloadState != expectedState || !bytes.Equal(record.Body, wire) || record.BodySHA256 != sha256.Sum256(wire) || record.BodyBytes != int64(len(wire)) || (!eof && record.PayloadReason != "response_closed_before_eof") || (eof && record.PayloadReason != "") {
						t.Fatalf("semantic success mislabeled logical evidence: digest=%s state=%s reason=%s", record.DigestScope, record.PayloadState, record.PayloadReason)
					}
				}
				if !found {
					t.Fatal("transform input record missing")
				}
			})
		}
	}
}
