package exchange

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
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
