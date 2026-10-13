package serverhost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

type serverEgressDefaultView struct {
	Revision  uint64                         `json:"revision"`
	Profile   *egressprofile.ProfileRevision `json:"profile"`
	Effective struct {
		Profile         egressprofile.ProfileRevision `json:"profile"`
		Source          string                        `json:"source"`
		DefaultRevision uint64                        `json:"defaultRevision"`
	} `json:"effective"`
}

// Catches a Runtime-owned defaults controller omitted from the real Server
// application, which the separate DesktopHost composition cannot exercise.
func TestServerOwnerEgressDefaultPersistsThroughActualControl(t *testing.T) {
	options := serverOptions(t, t.TempDir())
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	host, err := serverhost.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	base := "http://" + host.Status().ListenAddress
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	recovery, err := os.ReadFile(host.Status().RecoveryKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	setup := postJSON(t, client, base+servercontrol.WebSetupPath, "", servercontrol.WebSetup{Schema: servercontrol.WebSetupSchema, RecoveryKey: strings.TrimSpace(string(recovery)), Username: "egress-owner", Password: "synthetic-egress-owner-password"})
	clear(recovery)
	if setup.StatusCode != http.StatusCreated {
		setup.Body.Close()
		t.Fatalf("owner setup status%d", setup.StatusCode)
	}
	var session servercontrol.WebSession
	err = json.NewDecoder(setup.Body).Decode(&session)
	setup.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	request := func(method, path, token string, expected uint64, key, body string) *http.Response {
		t.Helper()
		r, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("If-Match", strconv.FormatUint(expected, 10))
			r.Header.Set("Idempotency-Key", key)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	read := func(token string) (int, serverEgressDefaultView) {
		t.Helper()
		response := request(http.MethodGet, "/api/v1/egress-default", token, 0, "", "")
		defer response.Body.Close()
		var view serverEgressDefaultView
		if response.StatusCode == 200 {
			if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
				t.Fatal(err)
			}
		}
		return response.StatusCode, view
	}
	status, initial := read(session.ReadToken)
	if status != 200 {
		t.Fatalf("actual Runtime Server owner GET status=%d, want200", status)
	}
	if initial.Revision != 1 || initial.Profile != nil || initial.Effective.Source != "builtin" || initial.Effective.DefaultRevision != 1 || !initial.Effective.Profile.Equal(egressprofile.Direct()) {
		t.Fatalf("initial default %+v", initial)
	}
	if status, _ := read(""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET status%d", status)
	}

	// Publishing/choosing a policy is configuration only: no upstream/SOCKS dial.
	published := request(http.MethodPut, "/api/v1/egress-profiles/profile.server-test", session.WriteToken, 0, "server-publish-egress-0001", `{"displayName":"Synthetic server exit","policy":{"proxy":{"kind":"socks5","endpoint":"127.0.0.1:7890"},"resolver":{"kind":"system","transport":"direct"}}}`)
	var profile egressprofile.ProfileRevision
	if published.StatusCode != 200 {
		published.Body.Close()
		t.Fatalf("publish status%d", published.StatusCode)
	}
	err = json.NewDecoder(published.Body).Decode(&profile)
	published.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "profile.server-test" || profile.Revision != 1 || profile.Policy.Proxy.Endpoint != "127.0.0.1:7890" {
		t.Fatal("published policy differs")
	}
	input := `{"profile":{"id":"profile.server-test","revision":1}}`
	saved := request(http.MethodPut, "/api/v1/egress-default", session.WriteToken, 1, "server-select-default-0001", input)
	var selected serverEgressDefaultView
	if saved.StatusCode != 200 {
		saved.Body.Close()
		t.Fatalf("actual Runtime Server owner PUT status%d", saved.StatusCode)
	}
	err = json.NewDecoder(saved.Body).Decode(&selected)
	saved.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if selected.Revision != 2 || selected.Profile == nil || !selected.Profile.Equal(profile) || selected.Effective.Source != "global" || selected.Effective.DefaultRevision != 2 || !selected.Effective.Profile.Equal(profile) {
		t.Fatalf("selected %+v", selected)
	}

	for _, token := range []string{session.ReadToken, ""} {
		denied := request(http.MethodPut, "/api/v1/egress-default", token, 2, "server-denied-clear-0001", `{"profile":null}`)
		denied.Body.Close()
		if denied.StatusCode != http.StatusUnauthorized {
			t.Fatalf("non-write PUT status%d", denied.StatusCode)
		}
	}
	status, after := read(session.ReadToken)
	beforeJSON, _ := json.Marshal(selected)
	afterJSON, _ := json.Marshal(after)
	if status != 200 || !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatal("denied mutation changed server default")
	}
	actual, err := host.Runtime().EgressDefaults().GetDefault(context.Background())
	if err != nil || actual.Revision != 2 || actual.Profile == nil || !actual.Profile.Equal(profile) {
		t.Fatalf("actual persisted setting %+v %v", actual, err)
	}
	resolved, err := host.Runtime().EgressDefaults().Resolve(context.Background(), egressprofile.Inherit(), egressprofile.Inherit())
	if err != nil || resolved.DefaultRevision() != 2 || resolved.Source() != egressprofile.SelectionGlobal || !resolved.Profile().Equal(profile) {
		t.Fatalf("actual resolved setting %+v %v", resolved, err)
	}
}
