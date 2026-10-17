package loopbackproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
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

func TestDownstreamStreamingCompletesWhileRawWriterAndBuffersSaturated(t *testing.T) {
	budget, _ := recording.NewBudget(recording.Limits{Records: 3, Bytes: 256 << 10})
	sink := &saturatedRawRepository{entered: make(chan struct{}), release: make(chan struct{})}
	manager, err := rawevidence.Open(context.Background(), rawevidence.Options{Budget: budget, Repository: sink, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)), Clock: rawevidence.SystemClock{}, Config: rawevidence.Config{MaximumQueueRecords: 3, MaximumQueueBytes: 1 << 20, MaximumBatchRecords: 1, MaximumBatchBytes: 1 << 20, FlushInterval: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(sink.release); manager.Shutdown(context.Background()) }()
	ctx := rawevidence.Context{ScopeKind: rawevidence.ScopeRuntime, ExchangeID: "stream-budget", Recording: rawevidence.RecordingFull, RetentionDays: 1}
	manager.Observe(context.Background(), rawevidence.Observation{Context: ctx, Layer: rawevidence.LayerProviderEgress, Complete: true, Representation: "http_message"})
	<-sink.entered
	expected := bytes.Repeat([]byte("data: unchanged\n\n"), 16384)
	output := httptest.NewRecorder()
	first := newHTTPDownstream(output, httpDownstreamOptions{Observer: manager, Context: ctx, MaximumBodyBytes: 16 << 20})
	second := newHTTPDownstream(httptest.NewRecorder(), httpDownstreamOptions{Observer: manager, Context: ctx, MaximumBodyBytes: 16 << 20})
	envelope, _ := exchange.NewResponseEnvelope(exchange.ResponseModeEventStream, 200, http.Header{"Content-Type": {"text/event-stream"}})
	if err := first.Begin(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if err := second.Begin(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if budget.Snapshot().ReservedRecords != 3 {
		t.Fatal("stream buffers excluded from budget")
	}
	done := make(chan struct{})
	go func() { first.Write(context.Background(), expected); first.Finalize(); second.Finalize(); close(done) }()
	select {
	case <-done:
		if !bytes.Equal(output.Body.Bytes(), expected) {
			t.Fatal("forwarded bytes changed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream waited for optional storage")
	}
	if got := budget.Snapshot(); got.ReservedBytes > 256<<10 || got.ReservedRecords > 3 {
		t.Fatalf("budget exceeded: %+v", got)
	}
}

type ingressAdmissionObserver struct {
	*rawevidence.Manager
	prehashed bool
	captures  int
}

func (o *ingressAdmissionObserver) Observe(ctx context.Context, observation rawevidence.Observation) (rawevidence.Watermark, error) {
	o.prehashed = observation.DigestAvailable
	return o.Manager.Observe(ctx, observation)
}
func (o *ingressAdmissionObserver) Capture(meta rawevidence.CaptureMetadata, maximum int) *rawevidence.CaptureBuffer {
	o.captures++
	return o.Manager.Capture(meta, maximum)
}

func TestSaturatedIngressRefusesBeforeOptionalDigest(t *testing.T) {
	budget, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 1 << 20})
	held, _ := budget.TryReserve(recording.Content, 1)
	defer held.Release()
	repo := &saturatedRawRepository{entered: make(chan struct{}), release: make(chan struct{})}
	manager, err := rawevidence.Open(context.Background(), rawevidence.Options{Budget: budget, Repository: repo, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)), Clock: rawevidence.SystemClock{}, Config: rawevidence.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(context.Background())
	observer := &ingressAdmissionObserver{Manager: manager}
	handler := &Handler{rawEvidence: observer, rawBodyBytes: 8, rawTimeout: time.Second}
	request := httptest.NewRequest("POST", "https://fixture.test/ingress", nil)
	body := bytes.Repeat([]byte("input"), 1<<16)
	rawContext := rawevidence.Context{ScopeKind: rawevidence.ScopeRuntime, ExchangeID: "ingress-full", Recording: rawevidence.RecordingFull, RetentionDays: 1}
	if err := handler.observeClientIngress(request, rawContext, body); err != nil {
		t.Fatal(err)
	}
	if observer.prehashed {
		t.Fatal("saturated ingress computed digest before admission")
	}
	if observer.captures != 1 || budget.Snapshot().ByKind[recording.Raw].Offers[recording.DroppedCapacity].Records != 1 {
		t.Fatal("ingress did not refuse through bounded admission")
	}
}

type ingressStoredRepository struct {
	saturatedRawRepository
	records []rawevidence.StoredEnvelope
}

func (r *ingressStoredRepository) AppendBatch(_ context.Context, records []rawevidence.StoredEnvelope, _ time.Time) error {
	r.records = append(r.records, records...)
	return nil
}

func TestAdmittedIngressPrefixKeepsFullBodyDigest(t *testing.T) {
	for _, mode := range []rawevidence.RecordingMode{rawevidence.RecordingFull, rawevidence.RecordingMetadataOnly} {
		t.Run(string(mode), func(t *testing.T) {
			repo := &ingressStoredRepository{}
			manager, err := rawevidence.Open(context.Background(), rawevidence.Options{Repository: repo, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)), Clock: rawevidence.SystemClock{}, Config: rawevidence.DefaultConfig()})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Shutdown(context.Background())
			handler := &Handler{rawEvidence: manager, rawBodyBytes: 8, rawTimeout: time.Second}
			request := httptest.NewRequest("POST", "https://fixture.test/ingress", nil)
			request.Header.Set("Authorization", "Bearer private")
			body := []byte("request body whose retained prefix is shorter")
			rawContext := rawevidence.Context{ScopeKind: rawevidence.ScopeRuntime, ExchangeID: "ingress-prefix", Recording: mode, RetentionDays: 1}
			if err := handler.observeClientIngress(request, rawContext, body); err != nil {
				t.Fatal(err)
			}
			if err := manager.Flush(context.Background(), rawevidence.Watermark{}); err != nil {
				t.Fatal(err)
			}
			if len(repo.records) != 1 {
				t.Fatal("ingress record missing")
			}
			record := repo.records[0]
			if record.DigestScope != rawevidence.DigestFull || record.BodySHA256 != sha256.Sum256(body) || record.BodyBytes != int64(len(body)) {
				t.Fatal("ingress digest no longer covers full request")
			}
			if mode == rawevidence.RecordingFull && (!bytes.Equal(record.Body, body[:8]) || record.PayloadState != rawevidence.PayloadTruncated || record.PayloadReason != "request_payload_limit") {
				t.Fatal("ingress prefix truth changed")
			}
			if mode == rawevidence.RecordingMetadataOnly && (len(record.Body) != 0 || record.PayloadState != rawevidence.PayloadMetadataOnly) {
				t.Fatal("metadata-only retained body")
			}
			if bytes.Contains(record.PayloadMetadata, []byte("Bearer private")) {
				t.Fatal("ingress retained credential")
			}
		})
	}
}
