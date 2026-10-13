package desktophost_test

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDesktopHostUpdateCommandQueuesUnderHoldAndCancellationDrains(t *testing.T) {
	root := t.TempDir()
	host := startHost(t, hostOptions(t, newHostPaths(t, filepath.Join(root, "cache")), filepath.Join(root, "data")))
	defer shutdownHost(t, host)
	r := host.Runtime()
	if _, err := r.EnterOfflineHold(context.Background(), r.OfflineHoldSnapshot().Revision); err != nil {
		t.Fatal(err)
	}
	app := host.AppSession()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "POST", app.BaseURL+"/api/v1/product-update/check", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "vibermate://desktop")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+app.WriteToken)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 4 * time.Second}
	done := make(chan error, 1)
	go func() {
		response, err := client.Do(request)
		if response != nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		done <- err
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("real Desktop owner consumer failed join")
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for r.OfflineHoldSnapshot().QueuedByKind[offlinehold.EgressUpdate] != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("actual Desktop composition did not queue update %+v", r.OfflineHoldSnapshot())
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("real canceled Desktop request returned success")
		}
		done <- err
	case <-time.After(2 * time.Second):
		t.Fatal("Desktop request did not cancel")
	}
	for snapshot := r.OfflineHoldSnapshot(); snapshot.QueuedRequests != 0 || snapshot.ActiveActions != 0 || snapshot.ActiveEgress != 0; snapshot = r.OfflineHoldSnapshot() {
		if time.Now().After(deadline) {
			t.Fatal("Desktop canceled update retained consumers", snapshot)
		}
		time.Sleep(time.Millisecond)
	}
	page, err := r.EgressAttempts().List(context.Background(), egressaudit.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("Held Desktop update reached outbound audit", err)
	}
}
