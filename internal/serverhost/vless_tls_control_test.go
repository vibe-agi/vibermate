package serverhost_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
	"github.com/vibe-agi/vibermate/internal/vlessprofile"
)

func TestServerVLESSSettingsRequiresOwnerReadAuthority(t *testing.T) {
	options := serverOptions(t, t.TempDir())
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	host, err := serverhost.Start(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	base := "http://" + host.Status().ListenAddress
	key, err := os.ReadFile(host.Status().RecoveryKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	setup := postJSON(t, client, base+servercontrol.WebSetupPath, "", servercontrol.WebSetup{Schema: servercontrol.WebSetupSchema, RecoveryKey: strings.TrimSpace(string(key)), Username: "tls-owner", Password: "synthetic-tls-owner-password"})
	clear(key)
	var owner servercontrol.WebSession
	err = json.NewDecoder(setup.Body).Decode(&owner)
	setup.Body.Close()
	if err != nil || setup.StatusCode != 201 {
		t.Fatal("owner setup failed")
	}
	member, err := host.Runtime().RuntimeUsers().Create(context.Background(), runtimeuser.CreateCommand{Username: "tls-member", Password: []byte("synthetic-tls-member-password")})
	if err != nil {
		t.Fatal(err)
	}
	login := postJSON(t, client, base+servercontrol.WebSessionPath, "", servercontrol.WebLogin{Schema: servercontrol.WebLoginSchema, Username: member.Username, Password: "synthetic-tls-member-password"})
	var session servercontrol.WebSession
	err = json.NewDecoder(login.Body).Decode(&session)
	login.Body.Close()
	if err != nil || login.StatusCode != 201 {
		t.Fatal("member login failed")
	}
	_, err = host.Runtime().VLESSProfiles().Import(t.Context(), vlessprofile.ImportCommand{ID: "profile.server-tls", DisplayName: "TLS node", URI: "vless://11111111-2222-4333-8444-555555555555@node.fixture:443?security=tls&encryption=none", Resolver: egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverSystem, Transport: egressnetwork.ResolverTransportDirect}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, token string
		status      int
	}{{"owner", owner.ReadToken, 200}, {"member-read", session.ReadToken, 401}, {"member-write", session.WriteToken, 401}, {"anonymous", "", 401}} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, base+"/api/v1/egress-profiles/profile.server-tls/revisions/1/vless-settings", nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.status {
				t.Fatalf("settings status%d want%d", response.StatusCode, test.status)
			}
			if test.status == 200 {
				var d map[string]string
				if json.Unmarshal(data, &d) != nil || len(d) != 2 || d["security"] != "tls" || d["trustMode"] != "system" {
					t.Fatal("owner description unsafe")
				}
			}
		})
	}
}
