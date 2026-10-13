package nativemcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

type waitingBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *waitingBody) Read([]byte) (int, error) { <-b.closed; return 0, io.ErrClosedPipe }
func (b *waitingBody) Close() error             { b.once.Do(func() { close(b.closed) }); return nil }
func TestNativeInputCallHeaderAndStreamDeadlinesCloseOwnedReads(t *testing.T) {
	for _, edge := range []string{"input_call", "headers", "stream"} {
		t.Run(edge, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, DefaultLimits())
			f.handshake(t)
			// All JSON/202 setup calls and their consumers have joined. Apply
			// only the tested scalar before starting the next Serve worker.
			e := f.executor.(*executor)
			switch edge {
			case "input_call":
				e.limits.CallLifetime = 100 * time.Millisecond
			case "headers":
				// This budget includes real receipt verification before entry.
				e.limits.Headers = time.Second
			case "stream":
				e.limits.StreamIdle = 20 * time.Millisecond
			}
			if edge == "input_call" {
				request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
				body := &waitingBody{closed: make(chan struct{})}
				request.request.Body = body
				start := time.Now()
				terminal := f.executor.Serve(context.Background(), request, httptest.NewRecorder())
				if terminal.Outcome == OutcomeCompleted || time.Since(start) > time.Second || f.transport.sends() != 3 {
					t.Fatalf("unbounded borrowed input %+v", terminal)
				}
				select {
				case <-body.closed:
				default:
					t.Fatal("input deadline did not close the owned body")
				}
				return
			}
			type entryObservation struct {
				at, callDeadline time.Time
				ctx              context.Context
			}
			type terminalObservation struct {
				terminal Terminal
				at       time.Time
			}
			entered := make(chan entryObservation, 1)
			contextCanceled := make(chan time.Time, 1)
			var entryOnce sync.Once
			body := &waitingBody{closed: make(chan struct{})}
			f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
				entryOnce.Do(func() {
					deadline, _ := r.Context().Deadline()
					entered <- entryObservation{at: time.Now(), callDeadline: deadline, ctx: r.Context()}
				})
				if edge == "headers" {
					<-r.Context().Done()
					contextCanceled <- time.Now()
					return nil, r.Context().Err()
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
			}
			request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
			results := make(chan terminalObservation, 1)
			started := time.Now()
			go func() {
				terminal := f.executor.Serve(context.Background(), request, httptest.NewRecorder())
				results <- terminalObservation{terminal: terminal, at: time.Now()}
			}()
			f.approve(t, request.id)
			var entry entryObservation
			watchdog := time.Second
			if edge == "headers" {
				// Admission/Start are setup, not the post-entry observer phase.
				select {
				case entry = <-entered:
					if f.transport.sends() != 4 {
						t.Fatalf("headers setup entry sends=%d context=%v", f.transport.sends(), entry.ctx.Err())
					}
				case result := <-results:
					// A ready entry wins conceptually even if both channels are ready.
					select {
					case entry = <-entered:
						results <- result
					default:
						t.Fatalf("headers setup ended before entry: terminal=%+v sends=%d requestContext=%v elapsed=%s", result.terminal, f.transport.sends(), request.request.Context().Err(), result.at.Sub(started))
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("headers setup entry watchdog: sends=%d requestContext=%v handlerContext=unobserved elapsed=%s", f.transport.sends(), request.request.Context().Err(), time.Since(started))
				}
				t.Logf("headers entry=%s callDeadline=%s headerBudget=%s setupAge=%s context=%v", entry.at.Format(time.RFC3339Nano), entry.callDeadline.Format(time.RFC3339Nano), e.limits.Headers, entry.at.Sub(started), entry.ctx.Err())
				watchdog = 3 * time.Second
			}
			select {
			case result := <-results:
				terminal := result.terminal
				if entry.ctx == nil {
					select {
					case entry = <-entered:
					default:
						t.Fatalf("deadline fixture never entered transport: edge=%s terminal=%+v sends=%d elapsed=%s", edge, terminal, f.transport.sends(), result.at.Sub(started))
					}
				}
				if terminal.Outcome != OutcomeUncertain || f.transport.sends() != 4 {
					t.Fatalf("deadline after verified entry: edge=%s terminal=%+v sends=%d elapsed=%s", edge, terminal, f.transport.sends(), time.Since(started))
				}
				if edge == "stream" {
					select {
					case <-body.closed:
					default:
						t.Fatal("stream deadline did not close the owned body")
					}
				}
				if edge == "headers" {
					select {
					case canceledAt := <-contextCanceled:
						t.Logf("headers canceled=%s terminal=%s postEntry=%s terminalDrain=%s context=%v", canceledAt.Format(time.RFC3339Nano), result.at.Format(time.RFC3339Nano), result.at.Sub(entry.at), result.at.Sub(canceledAt), entry.ctx.Err())
					default:
						t.Fatal("header terminal lacked handler-context cancellation observation")
					}
				}
				t.Logf("edge=%s verified transport entry sends=4 outcome=%s elapsed=%s", edge, terminal.Outcome, time.Since(started))
			case <-time.After(watchdog):
				if entry.ctx == nil {
					select {
					case entry = <-entered:
					default:
					}
				}
				var canceledAt time.Time
				select {
				case canceledAt = <-contextCanceled:
				default:
				}
				var contextErr error
				if entry.ctx != nil {
					contextErr = entry.ctx.Err()
				}
				t.Fatalf("terminal observer watchdog: edge=%s sends=%d entry=%s callDeadline=%s handlerCanceled=%s handlerContext=%v elapsed=%s", edge, f.transport.sends(), entry.at, entry.callDeadline, canceledAt, contextErr, time.Since(started))
			}
		})
	}
}
