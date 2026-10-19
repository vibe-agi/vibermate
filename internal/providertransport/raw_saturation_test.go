package providertransport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

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

func TestProviderStreamingCompletesWhileRawWriterAndBuffersSaturated(t *testing.T) {
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
	first := newRawResponseBody(rawResponseBodyOptions{Source: io.NopCloser(bytes.NewReader(expected)), Observer: manager, Context: ctx, StatusCode: 200, MaximumBodyBytes: 16 << 20, Headers: http.Header{"Content-Type": {"text/event-stream"}}})
	second := newRawResponseBody(rawResponseBodyOptions{Source: io.NopCloser(bytes.NewReader(expected)), Observer: manager, Context: ctx, StatusCode: 200, MaximumBodyBytes: 16 << 20})
	if budget.Snapshot().ReservedRecords != 3 {
		t.Fatal("stream buffers excluded from budget")
	}
	done := make(chan []byte, 1)
	go func() { got, _ := io.ReadAll(first); first.Close(); second.Close(); done <- got }()
	select {
	case got := <-done:
		if !bytes.Equal(got, expected) {
			t.Fatal("forwarded bytes changed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream waited for optional storage")
	}
	if got := budget.Snapshot(); got.ReservedBytes > 256<<10 || got.ReservedRecords > 3 {
		t.Fatalf("budget exceeded: %+v", got)
	}
}
