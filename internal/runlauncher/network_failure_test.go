package runlauncher

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestDialTimeoutWithLiveCaptureContextIsConnectionFailure(t *testing.T) {
	ctx := context.Background()
	dialer := &net.Dialer{Deadline: time.Now().Add(-time.Second)}
	transport := &http.Transport{Proxy: nil, DialContext: dialer.DialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:1/api/v1/capture-runs", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(request)
	if err == nil {
		t.Fatal("expected expired dial deadline")
	}
	if ctx.Err() != nil {
		t.Fatal("capture context must still be alive")
	}
	got := classifyCreateFailure(err, false)
	t.Logf("live Capture context; actual dial failure=%v; classification=%v", err, got)
	if errors.Is(got, ErrCapturePreparationTimedOut) || !errors.Is(got, ErrRuntimeUnavailable) {
		t.Fatalf("network dial timeout was misclassified as Capture work timeout: %v", got)
	}
}
