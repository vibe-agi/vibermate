package desktophost_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

// Catches a configured Runtime whose actual authenticated Host forgot to
// inject the defaults controller into its control application.
func TestHostWiresEgressDefaultControlWithoutCLIPrivilege(t *testing.T) {
	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	host := startHost(t, hostOptions(t, paths, filepath.Join(root, "data")))
	defer shutdownHost(t, host)
	app := host.AppSession()
	response := controlRequest(t, app.BaseURL, http.MethodGet, "/api/v1/egress-default", app.ReadToken, "vibermate://desktop")
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("actual host default route status %d", response.StatusCode)
	}
	var result struct {
		Revision  uint64
		Effective struct{ Source string }
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || result.Revision != 1 || result.Effective.Source != "builtin" {
		t.Fatalf("host default %+v %v", result, err)
	}
	denied := controlRequest(t, app.BaseURL, http.MethodGet, "/api/v1/egress-default", "", "vibermate://desktop")
	defer denied.Body.Close()
	if denied.StatusCode != 401 && denied.StatusCode != 403 {
		t.Fatal("unauthenticated default route admitted")
	}
}
