package servertransport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
)

func TestTransportConfinesHTTPAndStreamsToTheSelectedRuntimeServer(t *testing.T) {
	t.Parallel()
	selected := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/status" {
			http.NotFound(writer, request)
			return
		}
		_, _ = io.WriteString(writer, "selected")
	}))
	defer selected.Close()
	escaped := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		t.Error("request escaped to an unselected Runtime Server")
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer escaped.Close()
	target, err := serverconnection.ParseTarget(selected.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := Open(Options{
		Target:         target,
		TrustDirectory: filepath.Join(t.TempDir(), "trust"),
		Clock:          fixedClock{now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)},
		Timeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer transport.Close()

	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, selected.URL+"/status", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.Do(request)
	if err != nil {
		t.Fatalf("Do(selected) error = %v", err)
	}
	response.Body.Close()

	escapeRequest, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, escaped.URL+"/status", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Do(escapeRequest); err == nil ||
		!strings.Contains(err.Error(), "selected Runtime Server") {
		t.Fatalf("Do(unselected) error = %v", err)
	}

	connection, err := transport.Dial(context.Background())
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	_ = connection.Close()
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }
