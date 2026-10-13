package loopbackproxy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

type livenessWriter struct {
	header             http.Header
	body               []byte
	count              int
	writeErr, flushErr error
	flushes            int
}

func (w *livenessWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (*livenessWriter) WriteHeader(int) {}
func (w *livenessWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.count >= 0 {
		n = w.count
	}
	if n >= 0 && n <= len(p) {
		w.body = append(w.body, p[:n]...)
	}
	return n, w.writeErr
}
func (w *livenessWriter) FlushError() error { w.flushes++; return w.flushErr }

type livenessWrappedWriter struct{ http.ResponseWriter }

func (w livenessWrappedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type livenessPlainWriter struct{ target *livenessWriter }

func (w livenessPlainWriter) Header() http.Header         { return w.target.Header() }
func (w livenessPlainWriter) WriteHeader(status int)      { w.target.WriteHeader(status) }
func (w livenessPlainWriter) Write(p []byte) (int, error) { return w.target.Write(p) }

type livenessNegativeWriter struct{ livenessPlainWriter }

func (livenessNegativeWriter) Write([]byte) (int, error) { return -1, nil }

type livenessLegacyFlusher struct{ livenessPlainWriter }

func (w livenessLegacyFlusher) Flush() { w.target.flushes++ }

func begunLiveness(t *testing.T, w http.ResponseWriter, selected bool) *httpDownstream {
	t.Helper()
	d := newHTTPDownstream(w, httpDownstreamOptions{ClientDialect: protocolspec.DialectOpenAIResponses, responsesDataKeepalive: selected, MaximumBodyBytes: 128, Context: rawevidence.Context{Recording: rawevidence.RecordingFull}})
	e, err := exchange.NewResponseEnvelope(exchange.ResponseModeEventStream, 200, http.Header{"Content-Type": {"text/event-stream"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Begin(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestKeepaliveSurfacesFlushFailureWithCommittedPrefix(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{
		{"ordinary", errors.New("fixture flush")},
		{"exact_not_supported", http.ErrNotSupported},
		{"wrapped_not_supported", fmt.Errorf("flush failed: %w", http.ErrNotSupported)},
		{"joined_connection_error", errors.Join(http.ErrNotSupported, io.ErrClosedPipe)},
	} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wrapped=%t", failure.name, wrapped), func(t *testing.T) {
				w := &livenessWriter{count: -1, flushErr: failure.err}
				var response http.ResponseWriter = w
				if wrapped {
					response = livenessWrappedWriter{w}
				}
				d := begunLiveness(t, response, true)
				err := d.Keepalive(context.Background())
				if !errors.Is(err, failure.err) || errors.Is(failure.err, http.ErrNotSupported) && !errors.Is(err, http.ErrNotSupported) || errors.Is(failure.err, io.ErrClosedPipe) && !errors.Is(err, io.ErrClosedPipe) {
					t.Errorf("observed flush failure chain lost: %v", err)
				}
				want := "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n"
				if w.flushes != 1 || string(w.body) != want || string(d.body) != want || d.total != int64(len(want)) || string(d.hash.Sum(nil)) != string(sha256Bytes([]byte(want))) || len(d.frames) != 1 || d.frames[0].Kind != rawevidence.FrameKeepalive || d.frames[0].Offset != 0 || d.frames[0].Length != int64(len(want)) {
					t.Fatal("invoked flush or exact committed prefix evidence changed")
				}
			})
		}
	}
}

func TestKeepalivePreservesAbsentAndLegacyFlushCapabilities(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("legacy=%t/wrapped=%t", legacy, wrapped), func(t *testing.T) {
				w := &livenessWriter{count: -1}
				var response http.ResponseWriter = livenessPlainWriter{w}
				if legacy {
					response = livenessLegacyFlusher{livenessPlainWriter{w}}
				}
				if wrapped {
					response = livenessWrappedWriter{response}
				}
				d := begunLiveness(t, response, true)
				before := w.flushes
				if err := d.Keepalive(context.Background()); err != nil {
					t.Fatal(err)
				}
				wantFlushes := before
				if legacy {
					wantFlushes++
				}
				if w.flushes != wantFlushes || string(w.body) != "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n" {
					t.Fatal("no-capability or legacy flushing behavior changed")
				}
			})
		}
	}
}
func sha256Bytes(b []byte) []byte { sum := sha256.Sum256(b); return sum[:] }

func TestKeepaliveRejectsWriteAfterTerminal(t *testing.T) {
	w := &livenessWriter{count: -1}
	d := begunLiveness(t, w, true)
	if err := d.Keepalive(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.Finalize(); err != nil {
		t.Fatal(err)
	}
	before := len(w.body)
	if err := d.Keepalive(context.Background()); err == nil || len(w.body) != before {
		t.Fatal("terminal downstream accepted late heartbeat")
	}
}

func TestKeepalivePreservesPartialWriteAndCancellation(t *testing.T) {
	for _, count := range []int{0, 7, 999} {
		w := &livenessWriter{count: count}
		d := begunLiveness(t, w, true)
		err := d.Keepalive(context.Background())
		if err == nil || count < 999 && !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("short/invalid count=%d err=%v", count, err)
		}
		if d.total != int64(len(w.body)) || string(d.hash.Sum(nil)) != string(sha256Bytes(w.body)) {
			t.Fatal("partial accounting differs")
		}
	}
	w := &livenessWriter{count: -1}
	d := begunLiveness(t, w, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Keepalive(ctx); !errors.Is(err, context.Canceled) || len(w.body) != 0 {
		t.Fatal("canceled tick wrote bytes")
	}
	if err := d.Keepalive(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := len(w.body)
	if err := d.Keepalive(ctx); !errors.Is(err, context.Canceled) || len(w.body) != before {
		t.Fatal("late canceled tick wrote bytes")
	}
}

func TestKeepaliveSelectionErrorsAndRetentionStayBounded(t *testing.T) {
	for _, selected := range []bool{false, true} {
		w := &livenessWriter{count: -1}
		d := newHTTPDownstream(livenessPlainWriter{w}, httpDownstreamOptions{responsesDataKeepalive: selected, MaximumBodyBytes: 10, Context: rawevidence.Context{Recording: rawevidence.RecordingFull}})
		if err := d.Keepalive(context.Background()); err == nil || len(w.body) != 0 {
			t.Fatal("unbegun heartbeat wrote")
		}
		e, _ := exchange.NewResponseEnvelope(exchange.ResponseModeEventStream, 200, http.Header{"Content-Type": {"text/event-stream"}})
		if err := d.Begin(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		d.options.responsesDataKeepalive = !selected // The choice is frozen at construction.
		for i := 0; i < 20; i++ {
			if err := d.Keepalive(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		want := ": keepalive\n\n"
		if selected {
			want = "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n"
		}
		if string(w.body[:len(want)]) != want || len(d.body) != 10 || len(d.frames) != 1 || d.frames[0].Length != 10 || d.total != int64(20*len(want)) || string(d.hash.Sum(nil)) != string(sha256Bytes(w.body)) {
			t.Fatal("immutable selection/capped prefix evidence differs")
		}
	}
	w := &livenessWriter{count: -1}
	d := begunLiveness(t, w, true)
	failure := errors.New("fixture encoding failure")
	d.keepaliveErr = failure
	if err := d.Keepalive(context.Background()); !errors.Is(err, failure) || len(w.body) != 0 || w.flushes != 0 {
		t.Fatal("encoding failure wrote/fell back")
	}
	jsonDownstream := newHTTPDownstream(livenessPlainWriter{w}, httpDownstreamOptions{responsesDataKeepalive: true})
	envelope, _ := exchange.NewResponseEnvelope(exchange.ResponseModeJSON, 200, nil)
	if err := jsonDownstream.Begin(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if err := jsonDownstream.Keepalive(context.Background()); err == nil || len(w.body) != 0 {
		t.Fatal("JSON response accepted heartbeat")
	}
	negative := begunLiveness(t, livenessNegativeWriter{livenessPlainWriter{w}}, true)
	if err := negative.Keepalive(context.Background()); err == nil || negative.total != 0 || len(negative.frames) != 0 {
		t.Fatal("negative write count recorded")
	}
}
