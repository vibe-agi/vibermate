package runlauncher

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// A missing credential check or forwarding the frontier credential instead of
// the current Runtime credential must fail at a real proxy boundary.
func TestLocalProxyFrontierAuthenticatesBeforeDial(t *testing.T) {
	var accepted atomic.Int32
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unauthorized request reached Runtime proxy")
		w.WriteHeader(http.StatusBadGateway)
	}))
	backend.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			accepted.Add(1)
		}
	}
	backend.Start()
	t.Cleanup(backend.Close)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-generation-1-capability")
	for _, authorization := range []string{"", "Basic Y2FwdHVyZTp3cm9uZw==", frontierBasic("other", frontier.Token()), frontierBasic("capture", frontier.Token()+"suffix")} {
		conn := dialLocalFrontier(t, frontier)
		_, err := fmt.Fprintf(conn, "GET http://model.invalid/probe HTTP/1.1\r\nHost: model.invalid\r\n%sConnection: close\r\n\r\n", optionalFrontierAuth(authorization))
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		conn.Close()
		if response.StatusCode != http.StatusProxyAuthRequired {
			t.Fatalf("unauthorized status=%d", response.StatusCode)
		}
	}
	if got := accepted.Load(); got != 0 {
		t.Fatalf("unauthorized traffic opened %d Runtime connections", got)
	}
}

// Changing model auth, cookies, target, or POST content at the frontier breaks
// the child-owned authentication contract even if the proxy responds 200.
func TestLocalProxyFrontierRewritesOnlyOuterProxyCredential(t *testing.T) {
	type observation struct{ method, target, host, proxyAuth, authorization, cookie, body string }
	observed := make(chan observation, 1)
	body := strings.Repeat("literal-model-body-", 8192)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		observed <- observation{r.Method, r.RequestURI, r.Host, r.Header.Get("Proxy-Authorization"), r.Header.Get("Authorization"), r.Header.Get("Cookie"), string(content)}
		w.Header().Set("X-Runtime-Generation", "generation-1")
		_, _ = io.WriteString(w, "generation-1-response")
	}))
	t.Cleanup(backend.Close)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-generation-1-capability")
	client := localFrontierHTTPClient(t, frontier)
	request, _ := http.NewRequest(http.MethodPost, "http://model.invalid/v1/responses?fixture=1", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer original-model-token")
	request.Header.Set("Cookie", "session=original-cookie")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	gotBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || string(gotBody) != "generation-1-response" || response.Header.Get("X-Runtime-Generation") != "generation-1" {
		t.Fatalf("response status=%d body=%q err=%v", response.StatusCode, gotBody, err)
	}
	got := <-observed
	if got != (observation{"POST", "http://model.invalid/v1/responses?fixture=1", "model.invalid", "Basic Y2FwdHVyZTpydW50aW1lLWdlbmVyYXRpb24tMS1jYXBhYmlsaXR5", "Bearer original-model-token", "session=original-cookie", body}) {
		t.Fatalf("Runtime received changed request: method=%q target=%q host=%q proxyAuthMatches=%t modelAuth=%q cookie=%q bodyMatches=%t", got.method, got.target, got.host, got.proxyAuth == "Basic Y2FwdHVyZTpydW50aW1lLWdlbmVyYXRpb24tMS1jYXBhYmlsaXR5", got.authorization, got.cookie, got.body == body)
	}
}

// A target swap must not retarget an established tunnel or leave its copier
// alive. Only a newly established connection can carry the new capability.
func TestLocalProxyFrontierReplacementClosesPinnedTunnel(t *testing.T) {
	firstEnded := make(chan struct{})
	first := frontierTunnelBackend(t, "generation-1", "Basic Y2FwdHVyZTpydW50aW1lLWdlbmVyYXRpb24tMS1jYXBhYmlsaXR5", firstEnded)
	second := frontierTunnelBackend(t, "generation-2", "Basic Y2FwdHVyZTpydW50aW1lLWdlbmVyYXRpb24tMi1jYXBhYmlsaXR5", nil)
	frontier := newTestLocalProxyFrontier(t, "generation-1", first.URL, "runtime-generation-1-capability")
	origin, token := frontier.Origin(), frontier.Token()
	old, reader := openFrontierTunnel(t, frontier)
	defer old.Close()
	assertFrontierTunnel(t, old, reader, "generation-1")
	if err := frontier.ReplaceTarget(localProxyTarget{Generation: "generation-2", ProxyOrigin: second.URL, ProxyToken: "runtime-generation-2-capability"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("old generation tunnel remained open after replacement")
	}
	select {
	case <-firstEnded:
	case <-time.After(2 * time.Second):
		t.Fatal("old Runtime tunnel was not closed")
	}
	if frontier.Origin() != origin || frontier.Token() != token {
		t.Fatal("child proxy endpoint or token changed")
	}
	current, nextReader := openFrontierTunnel(t, frontier)
	defer current.Close()
	assertFrontierTunnel(t, current, nextReader, "generation-2")
	frontier.Close() // Must also join a hijacked CONNECT handler.
}

// Withdrawing authority may interrupt an upload, but must never queue it for
// another Runtime. Seeing the prefix upstream before EOF proves streaming.
func TestLocalProxyFrontierDoesNotReplayInterruptedPOST(t *testing.T) {
	entered := make(chan string, 1)
	firstEnded := make(chan struct{})
	var firstEntries, secondEntries atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstEntries.Add(1)
		prefix := make([]byte, len("held-model-post-prefix"))
		_, err := io.ReadFull(r.Body, prefix)
		if err != nil {
			t.Error(err)
		}
		entered <- string(prefix)
		_, _ = io.Copy(io.Discard, r.Body)
		close(firstEnded)
	}))
	t.Cleanup(first.Close)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondEntries.Add(1)
		_, _ = io.WriteString(w, "new-request-only")
	}))
	t.Cleanup(second.Close)
	frontier := newTestLocalProxyFrontier(t, "generation-1", first.URL, "runtime-generation-1-capability")
	input, feed := io.Pipe()
	request, _ := http.NewRequest(http.MethodPost, "http://model.invalid/v1/responses", input)
	request.GetBody = nil
	result := make(chan error, 1)
	client := localFrontierHTTPClient(t, frontier)
	go func() {
		response, err := client.Do(request)
		if response != nil {
			response.Body.Close()
		}
		result <- err
	}()
	written := make(chan error, 1)
	go func() { _, err := io.WriteString(feed, "held-model-post-prefix"); written <- err }()
	select {
	case got := <-entered:
		if got != "held-model-post-prefix" {
			t.Fatalf("upload prefix changed: %q", got)
		}
	case <-time.After(3 * time.Second):
		feed.Close()
		t.Fatal("frontier buffered the unfinished model POST")
	}
	frontier.Withdraw()
	if err := frontier.ReplaceTarget(localProxyTarget{Generation: "generation-2", ProxyOrigin: second.URL, ProxyToken: "runtime-generation-2-capability"}); err != nil {
		t.Fatal(err)
	}
	feed.Close()
	<-written
	select {
	case <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("interrupted POST did not finish")
	}
	select {
	case <-firstEnded:
	case <-time.After(2 * time.Second):
		t.Fatal("old Runtime upload remained open")
	}
	if firstEntries.Load() != 1 || secondEntries.Load() != 0 {
		t.Fatalf("interrupted POST was replayed: first=%d second=%d", firstEntries.Load(), secondEntries.Load())
	}
	response, err := client.Get("http://model.invalid/fresh")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || secondEntries.Load() != 1 {
		t.Fatalf("new request did not use new Runtime: status=%d entries=%d", response.StatusCode, secondEntries.Load())
	}
}

// An unavailable target may never fall back to direct destination access, an
// ambient proxy, or a previously withdrawn target.
func TestLocalProxyFrontierWithdrawalFailsClosed(t *testing.T) {
	var entries atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entries.Add(1); w.WriteHeader(200) }))
	t.Cleanup(backend.Close)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-generation-1-capability")
	frontier.Withdraw()
	client := localFrontierHTTPClient(t, frontier)
	response, err := client.Get("http://model.invalid/withdrawn")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || entries.Load() != 0 {
		t.Fatalf("withdrawn frontier forwarded: status=%d entries=%d", response.StatusCode, entries.Load())
	}
}

func TestLocalProxyFrontierRejectsNonLoopbackOrMalformedReplacement(t *testing.T) {
	backend := frontierTunnelBackend(t, "generation-1", "Basic Y2FwdHVyZTpydW50aW1lLWdlbmVyYXRpb24tMS1jYXBhYmlsaXR5", nil)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-generation-1-capability")
	conn, reader := openFrontierTunnel(t, frontier)
	defer conn.Close()
	assertFrontierTunnel(t, conn, reader, "generation-1")
	for _, origin := range []string{"http://localhost:1234", "http://192.0.2.1:1234", "http://127.0.0.1:0", "http://127.0.0.1:1234/path", "https://127.0.0.1:1234", "http://capture:secret@127.0.0.1:1234"} {
		if err := frontier.ReplaceTarget(localProxyTarget{Generation: "generation-2", ProxyOrigin: origin, ProxyToken: "runtime-generation-2-capability"}); err == nil {
			t.Fatalf("invalid backend accepted: %s", origin)
		}
		if _, err := io.WriteString(conn, "still-generation-1\n"); err != nil {
			t.Fatal(err)
		}
		got, err := reader.ReadString('\n')
		if err != nil || got != "still-generation-1\n" {
			t.Fatalf("invalid replacement disturbed current tunnel: %q %v", got, err)
		}
	}
	frontier.Close()
}

func TestLocalProxyFrontierRejectsOversizedHeadersBeforeDial(t *testing.T) {
	var accepted atomic.Int32
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	backend.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			accepted.Add(1)
		}
	}
	backend.Start()
	t.Cleanup(backend.Close)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-generation-1-capability")
	conn := dialLocalFrontier(t, frontier)
	defer conn.Close()
	_, _ = fmt.Fprintf(conn, "GET http://model.invalid/ HTTP/1.1\r\nHost: model.invalid\r\nProxy-Authorization: %s\r\nX-Oversized: %s\r\n\r\n", frontierBasic("capture", frontier.Token()), strings.Repeat("h", 80<<10))
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestHeaderFieldsTooLarge || accepted.Load() != 0 {
		t.Fatalf("oversized request reached backend: status=%d accepts=%d", response.StatusCode, accepted.Load())
	}
}

func TestLocalProxyFrontierBoundsBackendResponseHeaders(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Oversized", strings.Repeat("h", 80<<10))
		w.WriteHeader(200)
	}))
	t.Cleanup(backend.Close)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-generation-1-capability")
	response, err := localFrontierHTTPClient(t, frontier).Get("http://model.invalid/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("oversized backend response accepted: %d", response.StatusCode)
	}
}

func TestLocalProxyFrontierStreamsResponseBeforeRuntimeCompletes(t *testing.T) {
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "data: first-event\n\n")
		w.(http.Flusher).Flush()
		<-release
	}))
	t.Cleanup(backend.Close)
	defer close(release)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-generation-1-capability")
	response, err := localFrontierHTTPClient(t, frontier).Get("http://model.invalid/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	got := make([]byte, len("data: first-event\n\n"))
	if _, err := io.ReadFull(response.Body, got); err != nil || string(got) != "data: first-event\n\n" {
		t.Fatalf("response buffered until completion: %q %v", got, err)
	}
}

func TestLocalProxyFrontierDoesNotFollowRedirectsOrAmbientProxy(t *testing.T) {
	var directEntries, ambientEntries atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { directEntries.Add(1); w.WriteHeader(200) }))
	t.Cleanup(destination.Close)
	ambient := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ambientEntries.Add(1); w.WriteHeader(200) }))
	t.Cleanup(ambient.Close)
	t.Setenv("HTTP_PROXY", ambient.URL)
	t.Setenv("HTTPS_PROXY", ambient.URL)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(302)
	}))
	t.Cleanup(backend.Close)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-generation-1-capability")
	client := localFrontierHTTPClient(t, frontier)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Get("http://model.invalid/redirect")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 302 || response.Header.Get("Location") != destination.URL || directEntries.Load() != 0 || ambientEntries.Load() != 0 {
		t.Fatalf("frontier followed redirect or ambient proxy: status=%d direct=%d ambient=%d", response.StatusCode, directEntries.Load(), ambientEntries.Load())
	}
}

func TestLocalProxyFrontierCloseCancelsResponseHeaderWait(t *testing.T) {
	entered := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	t.Cleanup(backend.Close)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-generation-1-capability")
	client := localFrontierHTTPClient(t, frontier)
	result := make(chan struct{})
	go func() {
		response, _ := client.Get("http://model.invalid/no-headers")
		if response != nil {
			response.Body.Close()
		}
		close(result)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Runtime request did not start")
	}
	frontier.Close()
	select {
	case <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not cancel Runtime header wait")
	}
}

// Real Runtime network approval can legitimately keep CONNECT pending. Virtual
// time proves the frontier does not add an independent five-second cutoff.
func TestLocalProxyFrontierDoesNotDeadlineRuntimeCONNECTApproval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		front, runtime := net.Pipe()
		defer front.Close()
		defer runtime.Close()
		done := make(chan struct{})
		go func() {
			defer close(done)
			time.Sleep(31 * time.Second)
			_, _ = io.WriteString(runtime, "HTTP/1.1 200 Connection Established\r\n\r\n")
			runtime.Close()
		}()
		response, _, err := readLocalProxyResponse(front, &http.Request{Method: http.MethodConnect})
		front.Close()
		runtime.Close()
		<-done
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("Runtime approval was cut short: response=%v err=%v", response, err)
		}
	})
}

func TestLocalProxyFrontierRefusesExcessConnectionsBeforeDial(t *testing.T) {
	var entries atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entries.Add(1); w.WriteHeader(200) }))
	t.Cleanup(backend.Close)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-generation-1-capability")
	for range 128 {
		conn := dialLocalFrontier(t, frontier)
		defer conn.Close()
		if _, err := io.WriteString(conn, "GET http://model.invalid/ HTTP/1.1\r\n"); err != nil {
			t.Fatal(err)
		}
	}
	excess := dialLocalFrontier(t, frontier)
	defer excess.Close()
	_, _ = fmt.Fprintf(excess, "GET http://model.invalid/ HTTP/1.1\r\nHost: model.invalid\r\nProxy-Authorization: %s\r\n\r\n", frontierBasic("capture", frontier.Token()))
	response, err := http.ReadResponse(bufio.NewReader(excess), nil)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || entries.Load() != 0 {
		t.Fatalf("excess connection reached Runtime: response=%v err=%v entries=%d", response, err, entries.Load())
	}
	frontier.Close()
}

func TestLocalProxyFrontierCloseJoinsIdleHeaderConnections(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	t.Cleanup(backend.Close)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-generation-1-capability")
	conn := dialLocalFrontier(t, frontier)
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET http://model.invalid/ HTTP/1.1\r\n")
	done := make(chan struct{})
	go func() { frontier.Close(); frontier.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not join partial-header client")
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("Close retained client connection")
	}
	if err := frontier.ReplaceTarget(localProxyTarget{Generation: "generation-2", ProxyOrigin: backend.URL, ProxyToken: "runtime-generation-2-capability"}); err == nil {
		t.Fatal("closed frontier accepted a new target")
	}
}

func newTestLocalProxyFrontier(t *testing.T, generation, origin, token string) *localProxyFrontier {
	t.Helper()
	frontier, err := startLocalProxyFrontier(localProxyTarget{Generation: generation, ProxyOrigin: origin, ProxyToken: token})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(frontier.Close)
	return frontier
}

func localFrontierHTTPClient(t *testing.T, frontier *localProxyFrontier) *http.Client {
	t.Helper()
	proxy, err := url.Parse(frontier.Origin())
	if err != nil {
		t.Fatal(err)
	}
	proxy.User = url.UserPassword("capture", frontier.Token())
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 8 * time.Second}
}

func dialLocalFrontier(t *testing.T, frontier *localProxyFrontier) net.Conn {
	t.Helper()
	parsed, err := url.Parse(frontier.Origin())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp4", parsed.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func openFrontierTunnel(t *testing.T, frontier *localProxyFrontier) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn := dialLocalFrontier(t, frontier)
	_, err := fmt.Fprintf(conn, "CONNECT model.invalid:443 HTTP/1.1\r\nHost: model.invalid:443\r\nProxy-Authorization: %s\r\nAuthorization: Bearer original-model-token\r\nCookie: session=original-cookie\r\n\r\n", frontierBasic("capture", frontier.Token()))
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("CONNECT response=%v err=%v", response, err)
	}
	return conn, reader
}

func assertFrontierTunnel(t *testing.T, conn net.Conn, reader *bufio.Reader, generation string) {
	t.Helper()
	got, err := reader.ReadString('\n')
	if err != nil || got != generation+"\n" {
		t.Fatalf("tunnel generation=%q err=%v", got, err)
	}
	payload := []byte{0x16, 0x03, 0x01, 0x00, 0x05, 'o', 'p', 'a', 'q', 'u', 'e', '\n'}
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	gotBytes := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, gotBytes); err != nil || !bytes.Equal(gotBytes, payload) {
		t.Fatalf("opaque tunnel changed %x %v", gotBytes, err)
	}
}

func frontierTunnelBackend(t *testing.T, generation, wantProxyAuth string, ended chan struct{}) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.RequestURI != "model.invalid:443" || r.Header.Get("Proxy-Authorization") != wantProxyAuth || r.Header.Get("Authorization") != "Bearer original-model-token" || r.Header.Get("Cookie") != "session=original-cookie" {
			t.Errorf("changed CONNECT metadata: method=%q target=%q authMatches=%t", r.Method, r.RequestURI, r.Header.Get("Proxy-Authorization") == wantProxyAuth)
			w.WriteHeader(400)
			return
		}
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if ended != nil {
			defer close(ended)
		}
		_, _ = fmt.Fprintf(buffer, "HTTP/1.1 200 Connection Established\r\n\r\n%s\n", generation)
		if err := buffer.Flush(); err != nil {
			return
		}
		_, _ = io.Copy(conn, buffer)
	}))
	t.Cleanup(server.Close)
	return server
}

func frontierBasic(user, token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))
}

func optionalFrontierAuth(value string) string {
	if value == "" {
		return ""
	}
	return "Proxy-Authorization: " + value + "\r\n"
}
