package desktopcontrol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vibe-agi/vibermate/internal/productruntime"
)

func TestStorageLocationUsesTheRuntimeDirectory(t *testing.T) {
	t.Parallel()
	location := productruntime.StorageLocation{
		Backend: "sqlite", DataDirectory: "/srv/vibermate data",
		DatabasePath: "/srv/vibermate data/runtime.db",
	}
	handler := Handler{storage: storageLocationFixture{location}}
	response := httptest.NewRecorder()
	handler.getStorageLocation(response, httptest.NewRequest(http.MethodGet, "/api/v1/storage", nil))
	var actual productruntime.StorageLocation
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil || actual != location || response.Code != http.StatusOK {
		t.Fatalf("storage: status=%d data=%s error=%v", response.Code, response.Body, err)
	}
	for _, path := range []string{"/api/v1/storage?path=/tmp", "/api/v1/storage?includeSecrets=true"} {
		response := httptest.NewRecorder()
		handler.getStorageLocation(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unexpected storage query accepted: %s", path)
		}
	}
	response = httptest.NewRecorder()
	handler.storage = nil
	handler.getStorageLocation(response, httptest.NewRequest(http.MethodGet, "/api/v1/storage", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing Runtime storage guessed a path: %d", response.Code)
	}
}

type storageLocationFixture struct {
	location productruntime.StorageLocation
}

func (fixture storageLocationFixture) StorageLocation() productruntime.StorageLocation {
	return fixture.location
}
