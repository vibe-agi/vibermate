package originaltransport_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestOriginalTransportPinsClientOriginAndStripsProxyCredentials(
	t *testing.T,
) {
	t.Parallel()

	coordinator := &recordingCoordinator{}
	transport := &roundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString("ok")),
		},
	}
	client, err := originaltransport.New(originaltransport.Options{
		Coordinator: coordinator,
		Transport:   transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)
	origin, err := originidentity.ParseClientOrigin("https://api.anthropic.com:443")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"value":1}`)
	headers := http.Header{
		"Authorization":       []string{"Bearer client-owned"},
		"Proxy-Authorization": []string{"Basic secret"},
		"Connection":          []string{"X-Remove"},
		"X-Remove":            []string{"remove"},
	}
	request, err := originaltransport.NewRequest(originaltransport.RequestOptions{
		RequestID:    "opaque-1",
		Kind:         offlinehold.EgressOpaque,
		Origin:       origin,
		Method:       http.MethodPost,
		Path:         "/v1/unknown",
		RawQuery:     "page=1",
		Headers:      headers,
		Body:         body,
		PayloadClass: protocolspec.OperationPayloadControl,
		ConnectionID: "connection-test",
		ParentID:     "original-request-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	body[0] = '!'
	headers.Set("Authorization", "mutated")
	response, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("response body = %q", data)
	}
	sent := transport.Request()
	if sent.URL.String() != origin.String()+"/v1/unknown?page=1" ||
		sent.Host != origin.HTTPAuthority() {
		t.Fatalf("outbound target URL=%q Host=%q", sent.URL, sent.Host)
	}
	if sent.Header.Get("Authorization") != "" {
		// Client clears the live header immediately after RoundTrip. The fixture
		// captures a clone below, so this branch documents the expected API value.
		t.Fatalf("live request retained Authorization after send")
	}
	captured := transport.Headers()
	if captured.Get("Authorization") != "Bearer client-owned" ||
		captured.Get("Proxy-Authorization") != "" ||
		captured.Get("X-Remove") != "" {
		t.Fatalf("captured outbound headers = %v", captured)
	}
	if string(transport.Body()) != `{"value":1}` {
		t.Fatalf("outbound body = %q", transport.Body())
	}
	acquire := coordinator.Request()
	if acquire.Target.Kind != offlinehold.EgressOpaque ||
		acquire.Target.TargetRef != origin.String() ||
		acquire.Target.NetworkOrigin != origin.String() ||
		acquire.Target.HTTPAuthority != origin.HTTPAuthority() ||
		acquire.Target.TLSServerName != origin.Host() ||
		acquire.SizeBytes != int64(len(`{"value":1}`)) {
		t.Fatalf("egress acquire = %+v", acquire)
	}
	if coordinator.ReleaseCount() != 1 {
		t.Fatalf("lease release count = %d", coordinator.ReleaseCount())
	}
}

func TestOriginalTransportShutdownClosesActiveResponseBody(t *testing.T) {
	t.Parallel()

	coordinator := &recordingCoordinator{}
	body := newBlockingBody()
	client, err := originaltransport.New(originaltransport.Options{
		Coordinator: coordinator,
		Transport: &roundTripper{response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := originalRequest(t, offlinehold.EgressAuxiliary)
	response, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, readErr := response.Body.Read(make([]byte, 1))
		readDone <- readErr
	}()
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("response body read did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case readErr := <-readDone:
		if !errors.Is(readErr, io.ErrClosedPipe) {
			t.Fatalf("blocked read error = %v", readErr)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked response read did not converge")
	}
	if coordinator.ReleaseCount() != 1 {
		t.Fatalf("lease release count = %d", coordinator.ReleaseCount())
	}
}

func originalRequest(
	t *testing.T,
	kind offlinehold.EgressKind,
) originaltransport.Request {
	t.Helper()
	origin, err := originidentity.ParseClientOrigin("https://api.anthropic.com:443")
	if err != nil {
		t.Fatal(err)
	}
	request, err := originaltransport.NewRequest(originaltransport.RequestOptions{
		RequestID:    "original-test",
		Kind:         kind,
		Origin:       origin,
		Method:       http.MethodGet,
		Path:         "/v1/status",
		PayloadClass: protocolspec.OperationPayloadControl,
		ConnectionID: "connection-test",
		ParentID:     "original-request-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

type recordingCoordinator struct {
	mu       sync.Mutex
	request  offlinehold.AcquireRequest
	releases int
}

func (coordinator *recordingCoordinator) Start(
	context.Context,
	offlinehold.RuntimeBinding,
) error {
	return nil
}

func (coordinator *recordingCoordinator) Acquire(
	_ context.Context,
	request offlinehold.AcquireRequest,
) (offlinehold.Lease, error) {
	coordinator.mu.Lock()
	coordinator.request = request
	coordinator.mu.Unlock()
	return &recordingLease{coordinator: coordinator}, nil
}

func (coordinator *recordingCoordinator) BeginAction(
	context.Context,
	offlinehold.ActionRequest,
) (*offlinehold.ActionLease, error) {
	return &offlinehold.ActionLease{}, nil
}

func (coordinator *recordingCoordinator) BeginShutdown() {}

func (coordinator *recordingCoordinator) Drain(context.Context) error {
	return nil
}

func (coordinator *recordingCoordinator) Snapshot() offlinehold.Snapshot {
	return offlinehold.Snapshot{State: offlinehold.StateOnline}
}

func (coordinator *recordingCoordinator) Request() offlinehold.AcquireRequest {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.request
}

func (coordinator *recordingCoordinator) ReleaseCount() int {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.releases
}

type recordingLease struct {
	once        sync.Once
	coordinator *recordingCoordinator
}

func (lease *recordingLease) Release() {
	lease.once.Do(func() {
		lease.coordinator.mu.Lock()
		lease.coordinator.releases++
		lease.coordinator.mu.Unlock()
	})
}

type roundTripper struct {
	mu       sync.Mutex
	response *http.Response
	request  *http.Request
	headers  http.Header
	body     []byte
}

func (transport *roundTripper) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.request = request
	transport.headers = request.Header.Clone()
	transport.body = bytes.Clone(data)
	transport.mu.Unlock()
	return transport.response, nil
}

func (transport *roundTripper) Request() *http.Request {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.request
}

func (transport *roundTripper) Headers() http.Header {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.headers.Clone()
}

func (transport *roundTripper) Body() []byte {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return bytes.Clone(transport.body)
}

type blockingBody struct {
	started   chan struct{}
	closeOnce sync.Once
	closed    chan struct{}
}

func newBlockingBody() *blockingBody {
	return &blockingBody{
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (body *blockingBody) Read([]byte) (int, error) {
	select {
	case <-body.started:
	default:
		close(body.started)
	}
	<-body.closed
	return 0, io.ErrClosedPipe
}

func (body *blockingBody) Close() error {
	body.closeOnce.Do(func() {
		close(body.closed)
	})
	return nil
}

func shutdownClient(t *testing.T, client *originaltransport.Client) {
	t.Helper()
	if err := client.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown client: %v", err)
	}
}
