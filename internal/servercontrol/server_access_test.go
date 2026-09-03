package servercontrol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		len(access.Targets) != 1 || access.Targets[0] != "192.168.1.44:9666" {
		t.Fatalf("access = %#v", access)
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
		{"server.local:9666"},
		{"192.168.1.44:9666", "192.168.1.44:9666"},
	} {
		if _, err := NewServerAccess(ServerAccessOptions{
			Transport: "http", Targets: targets,
		}); err == nil {
			t.Fatalf("NewServerAccess accepted targets %#v", targets)
		}
	}
}
