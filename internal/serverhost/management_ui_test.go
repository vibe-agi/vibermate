package serverhost

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
			response.Header().Get("Cache-Control") != "no-cache" ||
			response.Header().Get("X-Frame-Options") != "DENY" ||
			response.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" {
			t.Fatalf(
				"GET %s status=%d headers=%v",
				requestPath,
				response.Code,
				response.Header(),
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

func TestManagementUICompressesMainBundleWithoutBreakingPlainOrRange(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := strings.Repeat("console.log('synthetic');\n", 1000)
	for name, content := range map[string]string{
		"index.html": "<!doctype html>", "main.dart.js": body,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := newManagementUI(root)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/main.dart.js", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	compressed := httptest.NewRecorder()
	handler.ServeHTTP(compressed, request)
	if compressed.Code != http.StatusOK ||
		compressed.Header().Get("Content-Encoding") != "gzip" ||
		compressed.Header().Get("Content-Length") != "" ||
		compressed.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("compressed response: %d %v", compressed.Code, compressed.Header())
	}
	reader, err := gzip.NewReader(compressed.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil || string(decoded) != body {
		t.Fatalf("compressed body mismatch: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	request = httptest.NewRequest(http.MethodGet, "/main.dart.js", nil)
	request.Header.Set("Accept-Encoding", "gzip;q=0.00")
	plain := httptest.NewRecorder()
	handler.ServeHTTP(plain, request)
	if plain.Code != http.StatusOK || plain.Header().Get("Content-Encoding") != "" ||
		plain.Body.String() != body {
		t.Fatalf("plain response: %d %v", plain.Code, plain.Header())
	}

	request = httptest.NewRequest(http.MethodGet, "/main.dart.js", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("Range", "bytes=0-6")
	partial := httptest.NewRecorder()
	handler.ServeHTTP(partial, request)
	if partial.Code != http.StatusPartialContent ||
		partial.Header().Get("Content-Encoding") != "" ||
		partial.Body.String() != body[:7] {
		t.Fatalf("range response: %d %v", partial.Code, partial.Header())
	}

	request = httptest.NewRequest(http.MethodHead, "/main.dart.js", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, request)
	if head.Code != http.StatusOK || head.Header().Get("Content-Encoding") != "gzip" ||
		head.Body.Len() != 0 {
		t.Fatalf("HEAD response: %d %v body=%d", head.Code, head.Header(), head.Body.Len())
	}
}
