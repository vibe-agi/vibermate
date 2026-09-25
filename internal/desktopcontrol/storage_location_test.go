package desktopcontrol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

func TestStorageLocationUsesTheRuntimeDirectory(t *testing.T) {
	t.Parallel()
	available := uint64(8 << 30)
	location := productruntime.StorageLocation{
		Backend: "sqlite", DataDirectory: "/srv/vibermate data",
		DatabasePath:  "/srv/vibermate data/runtime.db",
		CollectedAt:   time.Unix(1_790_000_000, 0).UTC(),
		DatabaseBytes: 4096, WALBytes: 1024, SharedMemoryBytes: 512,
		EvidenceBytes: 2048, ReusableBytes: 256,
		FilesystemAvailableBytes: &available,
		LowSpaceThresholdBytes:   1 << 30, CapacityState: "healthy",
	}
	handler := Handler{storage: &storageLocationFixture{location: location}}
	response := httptest.NewRecorder()
	handler.getStorageLocation(response, httptest.NewRequest(http.MethodGet, "/api/v1/storage", nil))
	var actual productruntime.StorageLocation
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil || !reflect.DeepEqual(actual, location) || response.Code != http.StatusOK {
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
	location     productruntime.StorageLocation
	cleanup      resourcedeletion.Released
	cleanupCalls int
}

func (fixture *storageLocationFixture) StorageLocation(context.Context) (productruntime.StorageLocation, error) {
	return fixture.location, nil
}

func (fixture *storageLocationFixture) CleanupExpiredStorage(context.Context) (resourcedeletion.Released, error) {
	fixture.cleanupCalls++
	return fixture.cleanup, nil
}

func (fixture *storageLocationFixture) EvidenceArchivePreview(context.Context) (resourcedeletion.Released, error) {
	return fixture.cleanup, nil
}

func TestExpiredStorageCleanupIsIdempotent(t *testing.T) {
	t.Parallel()
	fixture := &storageLocationFixture{cleanup: resourcedeletion.Released{
		Exchanges: 3, Envelopes: 12,
	}}
	handler := Handler{storage: fixture, idempotent: newIdempotencyCache()}
	preview := httptest.NewRecorder()
	handler.previewArchiveClear(
		preview,
		httptest.NewRequest(http.MethodGet, "/api/v1/evidence/actions/clear", nil),
	)
	if preview.Code != http.StatusOK ||
		!strings.Contains(preview.Body.String(), `"exchanges":3`) {
		t.Fatalf("preview: status=%d body=%s", preview.Code, preview.Body.String())
	}
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/storage/actions/cleanup-expired",
			nil,
		)
		request.Header.Set("If-Match", "0")
		request.Header.Set("Idempotency-Key", "storage-cleanup-test-key")
		response := httptest.NewRecorder()
		handler.cleanupExpiredStorage(response, request)
		if response.Code != http.StatusOK ||
			!strings.Contains(response.Body.String(), `"exchanges":3`) ||
			!strings.Contains(response.Body.String(), `"envelopes":12`) {
			t.Fatalf("attempt %d: status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	if fixture.cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", fixture.cleanupCalls)
	}
}
