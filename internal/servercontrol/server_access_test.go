package servercontrol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerAccessDescribesReusableRuntimeUserLoginWithoutApprovalMode(t *testing.T) {
	t.Parallel()
	handler, err := NewServerAccess(ServerAccessOptions{Transport: "http"})
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
		access.SessionPolicy != ReusableLoginSessionPolicy {
		t.Fatalf("access = %#v", access)
	}
}

func TestServerAccessRejectsMutationAndInvalidTransport(t *testing.T) {
	t.Parallel()
	if _, err := NewServerAccess(ServerAccessOptions{Transport: "ftp"}); err == nil {
		t.Fatal("NewServerAccess() accepted unsupported transport")
	}
	handler, err := NewServerAccess(ServerAccessOptions{Transport: "https"})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, ServerAccessPath, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("PATCH status = %d", response.Code)
	}
}
