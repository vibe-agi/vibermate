package nativemcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

// The parser must finish the complete frame before it can observe this error.
// Close also unblocks the read on an actual executor cancellation.
type gatedURLBody struct {
	*strings.Reader
	after      string
	returnedAt time.Time
	fail       chan error
	closed     chan struct{}
	once       sync.Once
}

func (b *gatedURLBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err != io.EOF {
		return n, err
	}
	select {
	case err := <-b.fail:
		if err == nil {
			b.Reader = strings.NewReader(b.after)
			b.after = ""
			return b.Read(p)
		}
		return 0, err
	case <-b.closed:
		return 0, io.ErrClosedPipe
	}
}
func (b *gatedURLBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

func TestNativeAcceptedGETURLSurvivesLaterDisconnect(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	wire := `{"jsonrpc":"2.0","id":18,"method":"elicitation/create","params":{"mode":"url","message":"Synthetic consent","url":"https://example.invalid/synthetic","elicitationId":"accepted-before-break"}}`
	body := &gatedURLBody{Reader: strings.NewReader("data: " + wire + "\n\n"), fail: make(chan error, 1), closed: make(chan struct{})}
	bodies := make(chan io.ReadCloser, 2)
	bodies <- body
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		if r.Method == "GET" {
			select {
			case next := <-bodies:
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: next}, nil
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	e := f.executor.(*executor)
	e.mu.Lock()
	var s *session
	for _, candidate := range e.scopes[f.capture.Key()].sessions {
		s = candidate
	}
	e.mu.Unlock()
	s.mu.Lock()
	baseline := s.retained
	s.mu.Unlock()
	get := f.captured(t, "GET", "", "synthetic-session")
	done := make(chan Terminal, 1)
	go func() {
		got := f.executor.Serve(context.Background(), get, httptest.NewRecorder())
		body.returnedAt = time.Now()
		done <- got
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		ready := s.parents["18"] != nil
		s.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("authenticated URL observation missing")
		}
		time.Sleep(time.Millisecond)
	}
	child := f.captured(t, "POST", `{"jsonrpc":"2.0","id":18,"result":{"action":"accept"}}`, "synthetic-session")
	replyDone := make(chan Terminal, 1)
	go func() { replyDone <- f.executor.Serve(context.Background(), child, httptest.NewRecorder()) }()
	f.approve(t, child.id)
	if got := <-replyDone; got.Outcome != OutcomeCompleted {
		t.Fatal(got)
	}
	s.mu.Lock()
	stream := s.streams[get.id]
	pending := s.elicitations["accepted-before-break"] == get.id
	parents := len(s.parents)
	expires := stream.expires
	charged := s.retained
	s.mu.Unlock()
	if !pending || parents != 0 {
		t.Fatalf("accept ordering not established: URL=%v parents=%d", pending, parents)
	}
	if charged != baseline+stream.charged+int64(128+len("accepted-before-break")) {
		t.Fatal("wrong retained URL charge")
	}
	select {
	case got := <-done:
		t.Fatalf("GET closed before gated break: %+v", got)
	default:
	}
	body.fail <- io.ErrUnexpectedEOF
	got := <-done
	if got.Outcome != OutcomeFailed || got.Reason != ReasonStreamDetached {
		t.Fatalf("accepted URL lost on complete-frame GET break: %+v", got)
	}
	s.mu.Lock()
	pending = s.elicitations["accepted-before-break"] == get.id
	unchanged := stream.expires.Equal(expires) && s.retained == charged
	s.mu.Unlock()
	if !pending || !unchanged {
		t.Fatal("break retired or renewed URL obligation")
	}
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil || scope.State != "active" {
		t.Fatal("break revoked scope", scope, err)
	}
	if err := f.store.NativeToolRepository().VerifyStartReceipt(context.Background(), stream.receipt, time.Now().UTC()); err == nil {
		t.Fatal("terminal GET receipt regained send authority")
	}
	var state, reason string
	var known bool
	if err := f.db.QueryRow(`SELECT outcome,error_class,byte_counts_known FROM runtime_egress_attempts WHERE attempt_id=?`, stream.receipt.AttemptID()).Scan(&state, &reason, &known); err != nil || state != "failed" || reason != "transport_failed" || known {
		t.Fatal("untruthful broken GET audit", state, reason, known, err)
	}
	before := f.transport.sends()
	bodies <- io.NopCloser(strings.NewReader("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/elicitation/complete\",\"params\":{\"elicitationId\":\"accepted-before-break\"}}\n\n"))
	completion := f.captured(t, "GET", "", "synthetic-session")
	if got := f.executor.Serve(context.Background(), completion, httptest.NewRecorder()); got.Outcome != OutcomeCompleted {
		t.Fatal("later URL completion rejected", got)
	}
	s.mu.Lock()
	_, pending = s.elicitations["accepted-before-break"]
	released := s.retained == baseline+stream.charged
	s.mu.Unlock()
	if pending || !released {
		t.Fatal("completion did not release exactly the URL charge")
	}
	if f.transport.sends() != before+1 {
		t.Fatal("completion repeated a reply")
	}
	var once, consumed int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, child.id).Scan(&once); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_approvals WHERE operation_id=? AND consumed_at_unix_ms IS NOT NULL`, child.id).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if once != 1 || consumed != 1 {
		t.Fatal(fmt.Sprintf("reply once=%d consumed=%d", once, consumed))
	}
}

func startReviewedURL(t *testing.T, f *nativeFixture, s *session, bodies chan io.ReadCloser, id int, urlID, action string) (*streamRecord, *gatedURLBody, <-chan Terminal) {
	t.Helper()
	wire := fmt.Sprintf("data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"method\":\"elicitation/create\",\"params\":{\"mode\":\"url\",\"message\":\"Synthetic consent\",\"url\":\"https://example.invalid/synthetic\",\"elicitationId\":%q}}\n\n", id, urlID)
	body := &gatedURLBody{Reader: strings.NewReader(wire), fail: make(chan error, 1), closed: make(chan struct{})}
	bodies <- body
	get := f.captured(t, "GET", "", "synthetic-session")
	done := make(chan Terminal, 1)
	go func() {
		got := f.executor.Serve(context.Background(), get, httptest.NewRecorder())
		body.returnedAt = time.Now()
		done <- got
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		ready := s.parents[fmt.Sprint(id)] != nil
		s.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("URL was not observed")
		}
		time.Sleep(time.Millisecond)
	}
	child := f.captured(t, "POST", fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"action":%q}}`, id, action), "synthetic-session")
	replyDone := make(chan Terminal, 1)
	go func() { replyDone <- f.executor.Serve(context.Background(), child, httptest.NewRecorder()) }()
	f.approve(t, child.id)
	if got := <-replyDone; got.Outcome != OutcomeCompleted {
		t.Fatal(got)
	}
	s.mu.Lock()
	stream := s.streams[get.id]
	_, parent := s.parents[fmt.Sprint(id)]
	pending := s.elicitations[urlID] == get.id
	s.mu.Unlock()
	if stream == nil || parent || pending != (action == "accept") {
		t.Fatal("reply-before-break ordering not established")
	}
	select {
	case got := <-done:
		t.Fatal("GET ended before release", got)
	default:
	}
	return stream, body, done
}

func TestNativeAcceptedGETURLDisconnectTerminalBoundaries(t *testing.T) {
	for _, edge := range []string{"decline", "cancel", "deadline", "owner", "off", "protocol", "unrelated"} {
		t.Run(edge, func(t *testing.T) {
			limits := DefaultLimits()
			limits.Pending = 2
			if edge == "unrelated" {
				limits.Pending = 3
			}
			if edge == "deadline" {
				limits.CallLifetime = 2 * time.Second
				limits.ConcurrentRPCsPerSession = 2
			}
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
			f.handshake(t)
			bodies := make(chan io.ReadCloser, 3)
			f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
				if r.Method == "GET" {
					select {
					case body := <-bodies:
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			e := f.executor.(*executor)
			e.mu.Lock()
			var s *session
			for _, candidate := range e.scopes[f.capture.Key()].sessions {
				s = candidate
			}
			e.mu.Unlock()
			s.mu.Lock()
			baseline := s.retained
			s.mu.Unlock()
			action := "accept"
			if edge == "decline" || edge == "cancel" {
				action = edge
			}
			stream, body, done := startReviewedURL(t, f, s, bodies, 18, "owned-url", action)
			var unrelatedDone <-chan Terminal
			if edge == "unrelated" {
				// The first stream still owns an accepted URL. A second stream whose URL
				// was declined must not borrow that obligation to survive its own break.
				unrelatedDone = done
				stream, body, done = startReviewedURL(t, f, s, bodies, 19, "declined-url", "decline")
				s.mu.Lock()
				other := s.elicitations["owned-url"]
				s.mu.Unlock()
				if other == "" || other == stream.operation {
					t.Fatal("unrelated URL isolation fixture invalid")
				}
			}
			switch edge {
			case "deadline": // The real owned timer must end the still-attached GET.
			case "owner", "off":
				reason := RevocationOwnerChanged
				if edge == "off" {
					reason = RevocationPermissionOff
				}
				if err := f.executor.Revoke(context.Background(), f.capture, reason); err != nil {
					t.Fatal(err)
				}
			case "protocol":
				body.after = "data: not-json\n\n"
				body.fail <- nil
			default:
				body.fail <- io.ErrUnexpectedEOF
			}
			select {
			case got := <-done:
				// The physical request deadline may precede the Store's original
				// claim-based observation deadline. Prove its eventual retirement
				// and full charge release below instead of equating the clocks.
				if got.Outcome == OutcomeCompleted || got.Reason == ReasonStreamDetached && (edge != "deadline" || got.Outcome != OutcomeFailed || !body.returnedAt.Before(stream.expires)) {
					t.Fatal("terminal or unrelated URL retained GET", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("terminal GET did not close")
			}
			if unrelatedDone != nil {
				select {
				case <-unrelatedDone:
				case <-time.After(time.Second):
					t.Fatal("revoked unrelated stream remained attached")
				}
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				s.mu.Lock()
				retained := s.retained
				closed := s.closed
				empty := len(s.elicitations) == 0 && len(s.parents) == 0 && len(s.streams) == 0
				s.mu.Unlock()
				want := baseline
				if closed {
					want = 0
				}
				if empty && retained == want {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("URL charges survived terminal boundary: bytes=%d want=%d empty=%v", retained, want, empty)
				}
				time.Sleep(time.Millisecond)
			}
			if f.store.NativeToolRepository().VerifyStartReceipt(context.Background(), stream.receipt, time.Now().UTC()) == nil {
				t.Fatal("terminal boundary revived GET receipt")
			}
			if edge == "deadline" {
				before := f.transport.sends()
				got, _ := f.serve(t, `{"jsonrpc":"2.0","id":18,"result":{"action":"accept"}}`, "synthetic-session")
				if got.Outcome == OutcomeCompleted || f.transport.sends() != before {
					t.Fatal("deadline admitted late URL reply", got)
				}
			} else {
				before := f.transport.sends()
				get := f.captured(t, "GET", "", "synthetic-session")
				got := f.executor.Serve(context.Background(), get, httptest.NewRecorder())
				if got.Outcome == OutcomeCompleted || f.transport.sends() != before {
					t.Fatal("revoked scope sent late GET", got)
				}
			}
		})
	}
}

func TestNativeCompletedAcceptedGETURLReusesCapacity(t *testing.T) {
	for _, beforeDetach := range []bool{false, true} {
		t.Run(fmt.Sprintf("before-detach=%v", beforeDetach), func(t *testing.T) {
			limits := DefaultLimits()
			limits.Pending = 2
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
			f.handshake(t)
			bodies := make(chan io.ReadCloser, 2)
			f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
				if r.Method == "GET" {
					select {
					case body := <-bodies:
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			e := f.executor.(*executor)
			e.mu.Lock()
			var s *session
			for _, candidate := range e.scopes[f.capture.Key()].sessions {
				s = candidate
			}
			e.mu.Unlock()
			s.mu.Lock()
			baseline := s.retained
			s.mu.Unlock()
			for i := 0; i < 3; i++ {
				urlID := fmt.Sprintf("completed-url-%d", i)
				stream, body, done := startReviewedURL(t, f, s, bodies, 18+i, urlID, "accept")
				if !beforeDetach {
					body.fail <- io.ErrUnexpectedEOF
					if got := <-done; got.Outcome != OutcomeFailed || got.Reason != ReasonStreamDetached {
						t.Fatal(got)
					}
				}
				bodies <- io.NopCloser(strings.NewReader(fmt.Sprintf("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/elicitation/complete\",\"params\":{\"elicitationId\":%q}}\n\n", urlID)))
				completion := f.captured(t, "GET", "", "synthetic-session")
				if got := f.executor.Serve(context.Background(), completion, httptest.NewRecorder()); got.Outcome != OutcomeCompleted {
					t.Fatalf("cycle %d completion blocked by obsolete GET record: %+v", i, got)
				}
				if beforeDetach {
					s.mu.Lock()
					attached := stream.attached && !stream.retired && s.streams[stream.operation] == stream
					s.mu.Unlock()
					if !attached {
						t.Fatal("completion retired still-attached GET")
					}
					body.fail <- io.EOF
					if got := <-done; got.Outcome != OutcomeCompleted {
						t.Fatal(got)
					}
				}
			}
			s.mu.Lock()
			released := len(s.streams) == 0 && len(s.parents) == 0 && len(s.elicitations) == 0 && s.retained == baseline
			s.mu.Unlock()
			if !released {
				t.Fatal("resolved URLs did not release exact stream records, slots and retained bytes")
			}
			if f.transport.sends() != 12 {
				t.Fatalf("extra callback/completion send: %d", f.transport.sends())
			}
		})
	}
}
