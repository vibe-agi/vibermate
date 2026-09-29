package serverhost_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/ipallowlist"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

type ipAllowlistView struct {
	Revision      int64    `json:"revision"`
	Ranges        []string `json:"ranges"`
	ClientAddress string   `json:"clientAddress"`
}

func TestOwnerManagesTheServerIPAllowlist(t *testing.T) {
	root := t.TempDir()
	options := serverOptions(t, root)
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	host, err := serverhost.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + host.Status().ListenAddress
	client := &http.Client{Timeout: 10 * time.Second}
	key, err := os.ReadFile(host.Status().RecoveryKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	setup := postJSON(t, client, base+servercontrol.WebSetupPath, "", servercontrol.WebSetup{
		Schema: servercontrol.WebSetupSchema, RecoveryKey: strings.TrimSpace(string(key)),
		Username: "owner", Password: "synthetic-owner-password",
	})
	var session servercontrol.WebSession
	if err := json.NewDecoder(setup.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	setup.Body.Close()

	read := func(bearer string) (int, ipAllowlistView) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, base+servercontrol.ServerIPAllowlistPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var view ipAllowlistView
		if response.StatusCode == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
				t.Fatal(err)
			}
		}
		return response.StatusCode, view
	}

	if status, _ := read(""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous GET status=%d", status)
	}
	status, view := read(session.ReadToken)
	if status != http.StatusOK || view.Revision != 0 || len(view.Ranges) != 0 || view.ClientAddress != "127.0.0.1" {
		t.Fatalf("Owner GET status=%d view=%+v", status, view)
	}
	input := map[string]any{"revision": 0, "ranges": []string{"203.0.113.0/24"}}
	if response := sendJSON(t, client, http.MethodPut, base+servercontrol.ServerIPAllowlistPath,
		session.ReadToken, input); response.StatusCode != http.StatusUnauthorized {
		response.Body.Close()
		t.Fatalf("PUT with a read capability status=%d", response.StatusCode)
	}
	// Loopback is always allowed, so the Owner on this machine keeps access.
	saved := sendJSON(t, client, http.MethodPut, base+servercontrol.ServerIPAllowlistPath,
		session.WriteToken, input)
	saved.Body.Close()
	if saved.StatusCode != http.StatusOK {
		t.Fatalf("Owner PUT status=%d", saved.StatusCode)
	}
	if status, view := read(session.ReadToken); status != http.StatusOK || view.Revision != 1 ||
		!slices.Equal(view.Ranges, []string{"203.0.113.0/24"}) {
		t.Fatalf("GET after PUT status=%d view=%+v", status, view)
	}
	if host.Status().IPAllowlistRanges != 1 {
		t.Fatalf("status ipAllowlistRanges = %d", host.Status().IPAllowlistRanges)
	}

	// The list survives a restart.
	shutdownServer(t, host)
	restart := serverOptions(t, root)
	restart.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	host, err = serverhost.Start(context.Background(), restart)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	if host.Status().IPAllowlistRanges != 1 {
		t.Fatalf("restarted status ipAllowlistRanges = %d", host.Status().IPAllowlistRanges)
	}

	// `vibermated server ip-allowlist clear` reaches the running Server.
	adminDirectory := filepath.Join(root, "data", "server-admin")
	if _, err := ipallowlist.Clear(adminDirectory, time.Now); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for host.Status().IPAllowlistRanges != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the running Server did not pick up the cleared list")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestServerRefusesToTrustEveryAddressAsALoadBalancer(t *testing.T) {
	t.Parallel()

	options := serverOptions(t, t.TempDir())
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	everyone, err := ipallowlist.Parse([]string{"10.0.0.0/8", "0.0.0.0/0"})
	if err != nil {
		t.Fatal(err)
	}
	options.TrustedProxies = everyone
	if host, err := serverhost.Start(context.Background(), options); err == nil {
		shutdownServer(t, host)
		t.Fatal("the Server trusted every address as a load balancer")
	}
}
