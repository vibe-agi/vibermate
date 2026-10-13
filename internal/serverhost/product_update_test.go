package serverhost_test

import (
	"context"
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestServerHostOwnerUpdateQueuesUnderHoldAndCancellationDrains(t *testing.T) {
	options := serverOptions(t, t.TempDir())
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	host, err := serverhost.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	base := "http://" + host.Status().ListenAddress
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 4 * time.Second}
	recovery, err := os.ReadFile(host.Status().RecoveryKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	setup := postJSON(t, client, base+servercontrol.WebSetupPath, "", servercontrol.WebSetup{Schema: servercontrol.WebSetupSchema, RecoveryKey: strings.TrimSpace(string(recovery)), Username: "update-owner", Password: "synthetic-update-owner-password"})
	clear(recovery)
	var session servercontrol.WebSession
	if setup.StatusCode != 201 || json.NewDecoder(setup.Body).Decode(&session) != nil {
		setup.Body.Close()
		t.Fatal("real owner setup failed", setup.StatusCode)
	}
	setup.Body.Close()
	r := host.Runtime()
	if _, err := r.EnterOfflineHold(context.Background(), r.OfflineHoldSnapshot().Revision); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "POST", base+"/api/v1/product-update/check", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+session.WriteToken)
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
			t.Error("real Server owner consumer failed join")
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for r.OfflineHoldSnapshot().QueuedByKind[offlinehold.EgressUpdate] != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("actual Server composition did not queue update %+v", r.OfflineHoldSnapshot())
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("real canceled Server request returned success")
		}
		done <- err
	case <-time.After(2 * time.Second):
		t.Fatal("Server request did not cancel")
	}
	for snapshot := r.OfflineHoldSnapshot(); snapshot.QueuedRequests != 0 || snapshot.ActiveActions != 0 || snapshot.ActiveEgress != 0; snapshot = r.OfflineHoldSnapshot() {
		if time.Now().After(deadline) {
			t.Fatal("Server canceled update retained consumers", snapshot)
		}
		time.Sleep(time.Millisecond)
	}
	page, err := r.EgressAttempts().List(context.Background(), egressaudit.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("Held Server update reached outbound audit", err)
	}
}
