package servercontrol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerAccessDescribesReusableRuntimeUserLoginWithoutApprovalMode(t *testing.T) {
	t.Parallel()
	handler, err := NewServerAccess(ServerAccessOptions{
		Transport: "http", Targets: []string{"192.168.1.44:9666"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, ServerAccessPath, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var access ServerAccess
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&access); err != nil {
		t.Fatal(err)
	}
	if access.Schema != ServerAccessSchema || access.Transport != "http" ||
		access.Authentication != RuntimeUserPasswordAuthentication ||
		access.SessionPolicy != ReusableLoginSessionPolicy ||
		len(access.Targets) != 1 || access.Targets[0] != "192.168.1.44:9666" ||
		access.TLS.Mode != "http" || access.TLS.State != "disabled" {
		t.Fatalf("access = %#v", access)
	}
}

func TestServerAccessReadsCurrentAutomaticTLSStatus(t *testing.T) {
	t.Parallel()
	state := "pending"
	handler, err := NewServerAccess(ServerAccessOptions{
		Transport: "https", Targets: []string{"runtime.example.com:443"},
		TLS: func() ServerTLS {
			status := ServerTLS{
				Mode: "automatic_tls", State: state,
				ServerName: "runtime.example.com", Challenge: "http_01",
			}
			if state == "ready" {
				status.Fingerprint = strings.Repeat("a", 64)
			}
			return status
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	state = "ready"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, ServerAccessPath, nil))
	var access ServerAccess
	if err := json.NewDecoder(response.Body).Decode(&access); err != nil {
		t.Fatal(err)
	}
	if access.TLS.State != "ready" || access.TLS.ServerName != "runtime.example.com" {
		t.Fatalf("TLS status = %+v", access.TLS)
	}
}

func TestServerAccessAcceptsAnExplicitDNSName(t *testing.T) {
	t.Parallel()
	handler, err := NewServerAccess(ServerAccessOptions{
		Transport: "https", Targets: []string{"runtime.example.com:443"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if handler.access.Targets[0] != "runtime.example.com:443" {
		t.Fatalf("targets = %#v", handler.access.Targets)
	}
}

func TestServerAccessRejectsMutationAndInvalidTransport(t *testing.T) {
	t.Parallel()
	if _, err := NewServerAccess(ServerAccessOptions{Transport: "ftp"}); err == nil {
		t.Fatal("NewServerAccess() accepted unsupported transport")
	}
	handler, err := NewServerAccess(ServerAccessOptions{
		Transport: "https", Targets: []string{"[fd00::8]:9666"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, ServerAccessPath, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("PATCH status = %d", response.Code)
	}
}

func TestServerAccessRejectsMissingOrNonConnectableTargets(t *testing.T) {
	t.Parallel()
	for _, targets := range [][]string{
		{},
		{"0.0.0.0:9666"},
		{"https://server.local:9666"},
		{"Server.Local:9666"},
		{"192.168.1.44:9666", "192.168.1.44:9666"},
	} {
		if _, err := NewServerAccess(ServerAccessOptions{
			Transport: "http", Targets: targets,
		}); err == nil {
			t.Fatalf("NewServerAccess accepted targets %#v", targets)
		}
	}
}
