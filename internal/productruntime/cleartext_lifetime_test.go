package productruntime

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

// A canceled real HTTP request must release the raw blind forwarding action,
// without needing the synthetic origin to send headers or close its socket.
func TestRuntimeCleartextCanceledHeadersOwnsUpstream(t *testing.T) {
	runRuntimeCleartextCancellation(t, false, false)
}

func TestRuntimeCleartextCanceledBodyOwnsUpstream(t *testing.T) {
	runRuntimeCleartextCancellation(t, true, false)
}

func TestRuntimeCleartextCancellationJoinsBodyClose(t *testing.T) {
	runRuntimeCleartextCancellation(t, false, true)
}

func runRuntimeCleartextCancellation(t *testing.T, responseBody, holdClose bool) {
	t.Helper()
	r, gate, credential := newCleartextLifetimeRuntime(t)

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	peerDone := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		defer c.Close()
		if _, err = http.ReadRequest(bufio.NewReader(c)); err != nil {
			peerDone <- err
			return
		}
		if responseBody {
			if _, err = fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: 16384\r\n\r\n%s", strings.Repeat("x", 8192)); err != nil {
				peerDone <- err
				return
			}
		}
		accepted <- c
		var b [1]byte
		_, err = c.Read(b[:])
		peerDone <- err
	}()
	handlerDone := make(chan struct{})
	barrier := &cleartextCloseBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	releaseClose := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer releaseClose()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(handlerDone)
		if holdClose {
			barrier.ReadCloser = req.Body
			req.Body = barrier
		}
		r.ProxyHandler().ServeHTTP(w, req)
	}))
	defer func() { releaseClose(); proxy.Close() }()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("capture", credential)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listener.Addr().String()+"/stalled", nil)
	if err != nil {
		t.Fatal(err)
	}
	clientDone := make(chan error, 1)
	bodyStarted := make(chan struct{})
	go func() {
		resp, err := (&http.Client{Transport: transport}).Do(req)
		if resp != nil {
			if responseBody {
				_, err = io.ReadFull(resp.Body, make([]byte, 8192))
				close(bodyStarted)
			}
			if err == nil {
				_, err = io.Copy(io.Discard, resp.Body)
			}
			_ = resp.Body.Close()
		}
		clientDone <- err
	}()
	var upstream net.Conn
	select {
	case upstream = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never received request")
	}
	// Failure cleanup force-closes the peer only after the assertions below.
	defer upstream.Close()
	if s := gate.Snapshot(); s.ActiveActions != 1 || s.ActiveEgress != 1 {
		t.Fatalf("live cleartext action/egress = %d/%d", s.ActiveActions, s.ActiveEgress)
	}
	if responseBody {
		select {
		case <-bodyStarted:
		case <-time.After(time.Second):
			t.Fatal("client did not receive response body prefix")
		}
	}
	if holdClose {
		barrier.block.Store(true)
	}
	cancel()
	if holdClose {
		select {
		case <-barrier.entered:
		case <-time.After(time.Second):
			t.Fatal("request cancellation never entered body Close")
		}
		if s := gate.Snapshot(); s.ActiveActions != 1 || s.ActiveEgress != 1 {
			t.Fatalf("unjoined body Close released action/egress: %+v", s)
		}
		page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].Attempt.Terminal() {
			t.Fatalf("terminal audit preceded body Close completion: %+v", page.Items)
		}
		drain, stopDrain := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer stopDrain()
		if err := r.proxy.Drain(drain); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("proxy drain with entered body Close = %v", err)
		}
		releaseClose()
	}
	select {
	case err = <-clientDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("client cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not cancel")
	}
	select {
	case <-handlerDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("canceled cleartext handler retained action/egress: %+v", gate.Snapshot())
	}
	select {
	case err = <-peerDone:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("upstream close = %v, want EOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream socket did not close")
	}
	stop, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err = r.proxy.Shutdown(stop); err != nil {
		t.Fatal(err)
	}
	if s := gate.Snapshot(); s.ActiveActions != 0 || s.ActiveEgress != 0 {
		t.Fatalf("drained cleartext action/egress = %d/%d", s.ActiveActions, s.ActiveEgress)
	}
	page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.Items[0].Attempt.Terminal() || page.Items[0].Attempt.Outcome() != egressaudit.OutcomeCanceled {
		t.Fatalf("cleartext terminal audit = %+v", page.Items)
	}
	timeline, err := r.ConnectionEvents().Timeline(t.Context(), page.Items[0].Attempt.ConnectionID())
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Events) != 3 || timeline.Events[2].Outcome != connectionevent.OutcomeCanceled {
		t.Fatalf("cleartext connection terminal = %+v", timeline.Events)
	}
}

func newCleartextLifetimeRuntime(t *testing.T, configure ...func(*Options)) (*Runtime, *offlinehold.Gate, string) {
	t.Helper()
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(t, hostcontract.Desktop(), gate)
	for _, apply := range configure {
		apply(&options)
	}
	r := startTestRuntime(t, options)
	t.Cleanup(func() { shutdownRuntime(t, r) })
	grant, err := r.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{
		Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Cleartext lifetime fixture",
		ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.CaptureAssignments().Create(t.Context(), captureassignment.CreateCommand{
		Capture: ref, EnvironmentID: environment.SystemTransparentID, Source: captureassignment.SourceSystemTransparent,
	}); err != nil {
		t.Fatal(err)
	}

	return r, gate, grant.Credential.Value()
}

// A raw upstream socket alone cannot interrupt Request.Write while it is
// waiting for the next bytes of an incomplete downstream upload.
func TestRuntimeCleartextShutdownOwnsUpload(t *testing.T) {
	t.Run("downstream read", func(t *testing.T) { runRuntimeCleartextUploadShutdown(t, false) })
	t.Run("upstream write", func(t *testing.T) { runRuntimeCleartextUploadShutdown(t, true) })
}

func runRuntimeCleartextUploadShutdown(t *testing.T, blockedWrite bool) {
	t.Helper()
	writes := make(chan (<-chan struct{}), 1024)
	r, gate, credential := newCleartextLifetimeRuntime(t, func(options *Options) {
		if blockedWrite {
			options.NetworkOptions.BaseDialer = cleartextWriteObserver{writes: writes}
		}
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	peerDone := make(chan error, 1)
	readPeer := make(chan struct{})
	var readPeerOnce sync.Once
	allowPeerRead := func() { readPeerOnce.Do(func() { close(readPeer) }) }
	defer allowPeerRead()
	go func() {
		c, err := listener.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		defer c.Close()
		req, err := http.ReadRequest(bufio.NewReader(c))
		if err != nil {
			peerDone <- err
			return
		}
		var first [1]byte
		if _, err = io.ReadFull(req.Body, first[:]); err != nil {
			peerDone <- err
			return
		}
		accepted <- c
		if blockedWrite {
			<-readPeer
		}
		_, err = io.Copy(io.Discard, req.Body)
		peerDone <- err
	}()
	handlerDone := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(handlerDone)
		r.ProxyHandler().ServeHTTP(w, req)
	}))
	defer proxy.Close()
	client, err := net.DialTimeout("tcp4", proxy.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	credentials := base64.StdEncoding.EncodeToString([]byte("capture:" + credential))
	length := 16384
	if blockedWrite {
		length = 8 << 20
	}
	if _, err = fmt.Fprintf(client, "POST http://%s/upload HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\nContent-Length: %d\r\n\r\n", listener.Addr(), listener.Addr(), credentials, length); err != nil {
		t.Fatal(err)
	}
	uploadDone := make(chan error, 1)
	if blockedWrite {
		go func() {
			_, err := io.CopyN(client, strings.NewReader(strings.Repeat("x", length)), int64(length))
			uploadDone <- err
		}()
	} else {
		if _, err = io.WriteString(client, strings.Repeat("x", 8192)); err != nil {
			t.Fatal(err)
		}
	}
	var upstream net.Conn
	select {
	case upstream = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never received initial upload byte")
	}
	defer upstream.Close()
	if blockedWrite {
		// Observe an actual TCP Write that cannot finish while the peer withholds
		// body reads. The small send buffer keeps the probe bounded and local.
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
	waitForBlockedWrite:
		for {
			select {
			case done := <-writes:
				timer := time.NewTimer(30 * time.Millisecond)
				select {
				case <-done:
					timer.Stop()
				case <-timer.C:
					break waitForBlockedWrite
				case <-deadline.C:
					t.Fatal("upstream Write never blocked")
				}
			case <-deadline.C:
				t.Fatal("upstream Write never started")
			}
		}
	}
	// Read the durable terminal after owner drainage and before Store disposal.
	checkTerminal := func(ctx context.Context) error {
		page, err := r.EgressAttempts().List(ctx, egressaudit.PageRequest{Limit: 10})
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || !page.Items[0].Attempt.Terminal() || page.Items[0].Attempt.Outcome() != egressaudit.OutcomeCanceled {
			return fmt.Errorf("cleartext shutdown terminal = %+v", page.Items)
		}
		timeline, err := r.storage.ConnectionEventRepository().Timeline(ctx, page.Items[0].Attempt.ConnectionID())
		if err != nil {
			return err
		}
		if len(timeline.Events) != 3 || timeline.Events[2].Outcome != connectionevent.OutcomeCanceled {
			return fmt.Errorf("cleartext shutdown connection terminal = %+v", timeline.Events)
		}
		return nil
	}
	for i := range r.cleanups.entries {
		if r.cleanups.entries[i].name == "sqlite" {
			closeStore := r.cleanups.entries[i].run
			r.cleanups.entries[i].run = func(ctx context.Context) error {
				return errors.Join(checkTerminal(ctx), closeStore(ctx))
			}
		}
	}
	stop, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err = r.Shutdown(stop); err != nil {
		t.Fatalf("Runtime shutdown retained incomplete cleartext upload: %v; gate=%+v", err, gate.Snapshot())
	}
	select {
	case <-handlerDone:
	default:
		t.Fatal("Runtime shutdown completed before cleartext handler joined")
	}
	allowPeerRead()
	if blockedWrite {
		// A full client upload may still be buffered locally; joining it requires
		// closing the downstream socket after the handler has already drained.
		_ = client.Close()
		select {
		case <-uploadDone:
		case <-time.After(time.Second):
			t.Fatal("client upload writer did not join")
		}
	}
	select {
	case err = <-peerDone:
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("upstream incomplete upload close = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream socket did not close")
	}
	if s := gate.Snapshot(); s.ActiveActions != 0 || s.ActiveEgress != 0 {
		t.Fatalf("Runtime shutdown retained action/egress = %d/%d", s.ActiveActions, s.ActiveEgress)
	}
}

// Request.Write also closes its upload normally; the barrier is armed only
// after the origin received it, to isolate an entered cancellation Close.
type cleartextCloseBarrier struct {
	io.ReadCloser
	block            atomic.Bool
	once             sync.Once
	entered, release chan struct{}
}

func (b *cleartextCloseBarrier) Close() error {
	if b.block.Load() {
		b.once.Do(func() { close(b.entered) })
		<-b.release
	}
	return b.ReadCloser.Close()
}

// Observe the real socket operation, without replacing its I/O or cancellation.
type cleartextWriteObserver struct{ writes chan<- <-chan struct{} }

func (o cleartextWriteObserver) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	if tcp, ok := c.(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(1024)
	}
	return &cleartextObservedConnection{Conn: c, writes: o.writes}, nil
}

type cleartextObservedConnection struct {
	net.Conn
	writes chan<- <-chan struct{}
}

func (c *cleartextObservedConnection) Write(p []byte) (int, error) {
	done := make(chan struct{})
	c.writes <- done
	defer close(done)
	return c.Conn.Write(p)
}

func TestRuntimeCleartextHealthyKeepAlive(t *testing.T) {
	r, _, credential := newCleartextLifetimeRuntime(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.WriteString(w, "healthy")
	}))
	defer origin.Close()
	proxy := httptest.NewServer(r.ProxyHandler())
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("capture", credential)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	for i := range 2 {
		var reused bool
		ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }})
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL+"/healthy", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || string(body) != "healthy" {
			t.Fatalf("healthy forward status=%d body=%q error=%v", response.StatusCode, body, err)
		}
		if i == 1 && !reused {
			t.Fatal("healthy cleartext completion prevented downstream keep-alive reuse")
		}
	}
}

// A normal dial failure leaves the context live. The final server upload Close
// can still drain a small incomplete body, so cancellation must remain armed
// until that synchronous Close returns.
func TestRuntimeCleartextShutdownInterruptsFailedDialBodyClose(t *testing.T) {
	r, gate, credential := newCleartextLifetimeRuntime(t)
	refused, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target := refused.Addr().String()
	if err = refused.Close(); err != nil {
		t.Fatal(err)
	}
	body := &cleartextCloseObserver{entered: make(chan struct{}), returned: make(chan struct{})}
	handlerDone := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(handlerDone)
		body.ReadCloser = req.Body
		req.Body = body
		r.ProxyHandler().ServeHTTP(w, req)
	}))
	defer proxy.Close()
	client, err := net.DialTimeout("tcp4", proxy.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	credentials := base64.StdEncoding.EncodeToString([]byte("capture:" + credential))
	if _, err = fmt.Fprintf(client, "POST http://%s/upload HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\nContent-Length: 16384\r\n\r\n%s", target, target, credentials, strings.Repeat("x", 8192)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-body.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("failed dial never entered synchronous upload Close")
	}
	select {
	case <-body.returned:
		t.Fatal("incomplete server upload Close returned before shutdown")
	default:
	}
	if s := gate.Snapshot(); s.ActiveActions != 1 || s.ActiveEgress != 0 {
		t.Fatalf("failed dial upload Close action/egress = %d/%d", s.ActiveActions, s.ActiveEgress)
	}
	page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Attempt.Terminal() {
		t.Fatalf("failed dial terminal preceded upload Close: %+v", page.Items)
	}
	var auditChecked bool
	for i := range r.cleanups.entries {
		if r.cleanups.entries[i].name == "sqlite" {
			closeStore := r.cleanups.entries[i].run
			r.cleanups.entries[i].run = func(ctx context.Context) (result error) {
				defer func() { result = errors.Join(result, closeStore(ctx)) }()
				page, err := r.EgressAttempts().List(ctx, egressaudit.PageRequest{Limit: 10})
				if err != nil {
					return err
				}
				if len(page.Items) != 1 || !page.Items[0].Attempt.Terminal() || page.Items[0].Attempt.Outcome() != egressaudit.OutcomeFailed || page.Items[0].Attempt.ErrorClass() != "tunnel_failed" {
					return fmt.Errorf("cleanup cancellation replaced failed dial terminal: %+v", page.Items)
				}
				timeline, err := r.storage.ConnectionEventRepository().Timeline(ctx, page.Items[0].Attempt.ConnectionID())
				if err != nil {
					return err
				}
				if len(timeline.Events) != 3 || timeline.Events[2].Outcome != connectionevent.OutcomeFailed || timeline.Events[2].ErrorClass != "tunnel_failed" {
					return fmt.Errorf("cleanup cancellation replaced failed dial connection terminal: %+v", timeline.Events)
				}
				auditChecked = true
				return nil
			}
		}
	}
	stop, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err = r.Shutdown(stop); err != nil {
		t.Fatalf("Runtime shutdown retained failed-dial upload Close: %v; gate=%+v", err, gate.Snapshot())
	}
	select {
	case <-handlerDone:
	default:
		t.Fatal("Runtime shutdown completed before failed-dial handler joined")
	}
	select {
	case <-body.returned:
	default:
		t.Fatal("Runtime shutdown completed before synchronous upload Close returned")
	}
	if !auditChecked {
		t.Fatal("failed dial terminal was not checked before Store disposal")
	}
	if s := gate.Snapshot(); s.ActiveActions != 0 || s.ActiveEgress != 0 {
		t.Fatalf("Runtime shutdown retained failed-dial action/egress = %d/%d", s.ActiveActions, s.ActiveEgress)
	}
}

// Observe actual net/http body Close entry/return without replacing its drain.
type cleartextCloseObserver struct {
	io.ReadCloser
	entered, returned         chan struct{}
	enteredOnce, returnedOnce sync.Once
}

func (b *cleartextCloseObserver) Close() error {
	b.enteredOnce.Do(func() { close(b.entered) })
	err := b.ReadCloser.Close()
	b.returnedOnce.Do(func() { close(b.returned) })
	return err
}
