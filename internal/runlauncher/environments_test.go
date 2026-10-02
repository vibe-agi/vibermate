package runlauncher

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
)

type catalogDiscovery struct{ url string }

func (discovery catalogDiscovery) Load() (localdiscovery.Session, error) {
	return localdiscovery.Session{BaseURL: discovery.url, ControlCredential: "fixture-control-credential"}, nil
}

func TestLauncherReadsScopedCatalogWithoutProxyOrManagementAuthority(t *testing.T) {
	for _, test := range []struct {
		name string
		page capturecontrol.CaptureEnvironments
		fail bool
	}{
		{"valid", capturecontrol.CaptureEnvironments{Schema: capturecontrol.EnvironmentsSchema, Items: []capturecontrol.CaptureEnvironment{{ID: "work", Name: "Work"}}}, false},
		{"empty", capturecontrol.CaptureEnvironments{Schema: capturecontrol.EnvironmentsSchema, Items: []capturecontrol.CaptureEnvironment{}}, false},
		{"old schema", capturecontrol.CaptureEnvironments{Schema: "old", Items: []capturecontrol.CaptureEnvironment{}}, true},
		{"null items", capturecontrol.CaptureEnvironments{Schema: capturecontrol.EnvironmentsSchema}, true},
		{"duplicate", capturecontrol.CaptureEnvironments{Schema: capturecontrol.EnvironmentsSchema, Items: []capturecontrol.CaptureEnvironment{{ID: "work", Name: "Work"}, {ID: "work", Name: "Other"}}}, true},
		{"blank name", capturecontrol.CaptureEnvironments{Schema: capturecontrol.EnvironmentsSchema, Items: []capturecontrol.CaptureEnvironment{{ID: "work", Name: " "}}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				if request.Method != http.MethodGet || request.URL.Path != capturecontrol.EnvironmentsPath ||
					request.Header.Get("Authorization") != "Bearer fixture-control-credential" ||
					request.Header.Get("Proxy-Authorization") != "" || request.Header.Get(capturecontrol.RunCapabilityHeader) != "" || len(body) != 0 {
					t.Error("catalog request carries the wrong authority or payload")
				}
				_ = json.NewEncoder(writer).Encode(test.page)
			}))
			defer server.Close()
			launcher, err := New(Config{Discovery: catalogDiscovery{url: server.URL}, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			page, err := launcher.Environments(context.Background())
			if (err != nil) != test.fail || !test.fail && len(page.Items) != len(test.page.Items) {
				t.Fatalf("catalog=%+v err=%v", page, err)
			}
		})
	}
}
