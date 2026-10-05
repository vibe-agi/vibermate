package loopbackproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

type heldHeartbeatWriter struct {
	livenessWriter
	flush            bool
	entered, release chan struct{}
	once             sync.Once
}

func (w *heldHeartbeatWriter) Write(p []byte) (int, error) {
	if w.flush {
		return w.livenessWriter.Write(p)
	}
	w.once.Do(func() { close(w.entered) })
	<-w.release
	w.body = append(w.body, p[:7]...)
	return 7, io.ErrClosedPipe
}
func (w *heldHeartbeatWriter) FlushError() error {
	w.flushes++
	if !w.flush {
		return nil
	}
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return io.ErrClosedPipe
}

func TestBlockedHeartbeatDrainsOnlyAfterOwnedWriterRelease(t *testing.T) {
	for _, flush := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "flush"}[flush], func(t *testing.T) {
			w := &heldHeartbeatWriter{livenessWriter: livenessWriter{count: -1}, flush: flush, entered: make(chan struct{}), release: make(chan struct{})}
			d := begunLiveness(t, w, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(w.release) }) }
			result := make(chan error, 1)
			joined := make(chan struct{})
			go func() { defer close(joined); result <- d.Keepalive(ctx) }()
			defer func() { cancel(); release(); <-joined }()
			select {
			case <-w.entered:
			case <-time.After(time.Second):
				t.Fatal("writer did not enter owned barrier")
			}
			cancel()
			select {
			case <-joined:
				t.Fatal("blocked arbitrary writer claimed cancellation self-release")
			default:
			}
			release()
			select {
			case <-joined:
			case <-time.After(time.Second):
				t.Fatal("writer did not drain after explicit release")
			}
			if err := <-result; !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("release error lost: %v", err)
			}
			want := "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n"
			if !flush {
				want = want[:7]
			}
			if string(w.body) != want || string(d.body) != want || d.total != int64(len(want)) || string(d.hash.Sum(nil)) != string(sha256Bytes([]byte(want))) || len(d.frames) != 1 || d.frames[0].Kind != rawevidence.FrameKeepalive || d.frames[0].Length != int64(len(want)) {
				t.Fatal("blocked write lost exact committed prefix")
			}
			if err := d.Finalize(); err != nil {
				t.Fatal(err)
			}
			beforeBytes, beforeFlush := len(w.body), w.flushes
			if err := d.Keepalive(context.Background()); err == nil || len(w.body) != beforeBytes || w.flushes != beforeFlush {
				t.Fatal("late write after joined/finalized writer")
			}
		})
	}
}

var _ http.ResponseWriter = (*heldHeartbeatWriter)(nil)
