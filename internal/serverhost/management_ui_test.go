package serverhost

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestManagementUIRevalidatesEveryPackagedMember(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for name, payload := range map[string]string{
		"index.html":           "<!doctype html>",
		"flutter_bootstrap.js": "bootstrap",
		"main.dart.js":         "application",
		"asset.bin":            "asset",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := newManagementUI(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, requestPath := range []string{
		"/", "/flutter_bootstrap.js", "/main.dart.js", "/asset.bin",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if response.Code != http.StatusOK ||
			response.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf(
				"GET %s status=%d cache=%q",
				requestPath,
				response.Code,
				response.Header().Get("Cache-Control"),
			)
		}
	}
}

func TestManagementUIRejectsSymbolicMembers(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "web")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "index.html"),
		[]byte("<!doctype html>"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "main.dart.js")); err != nil {
		t.Fatal(err)
	}

	if _, err := newManagementUI(root); err == nil {
		t.Fatal("management UI accepted a symbolic member")
	}
}
