package desktophost_test

import (
	"context"
	"database/sql"
	"errors"
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMissingControlPreservesPendingRootResetBytes(t *testing.T) {
	root := t.TempDir()
	paths, err := desktophost.NewPaths(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "data")
	first := startHost(t, hostOptions(t, paths, data))
	if err = localca.RequestRootReset(data, first.Runtime().LocalRootIdentity()); err != nil {
		t.Fatal(err)
	}
	shutdownHost(t, first)
	certificates := map[string][]byte{}
	entries, e := os.ReadDir(filepath.Join(data, "local-ca"))
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			path := filepath.Join(data, "local-ca", entry.Name())
			bytes, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			certificates[path] = bytes
		}
	}
	marker := filepath.Join(data, "root-reset-request.json")
	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(data, "runtime.db")); err != nil {
		t.Fatal(err)
	}
	next, err := desktophost.Start(context.Background(), hostOptions(t, paths, data))
	if err == nil {
		shutdownHost(t, next)
		t.Fatal("lost required control admitted")
	}
	for path, bytes := range certificates {
		after, e := os.ReadFile(path)
		if e != nil || string(after) != string(bytes) {
			t.Fatalf("required admission erased Root bytes: %v", e)
		}
	}
	after, readErr := os.ReadFile(marker)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("required admission consumed reset evidence: %v", readErr)
	}
}

func TestHostBootstrapAndControlDashboardIgnoreUnavailableInspection(t *testing.T) {
	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	options := hostOptions(t, paths, filepath.Join(root, "data"))
	data := options.Runtime.Paths.DataDirectory()
	// Establish legitimate required storage before making the optional role
	// deterministically unavailable. Fresh-root residual-state refusal remains intact.
	layout, err := func() (layout runtimedata.StorageLayout, result error) {
		if err := os.MkdirAll(data, 0700); err != nil {
			return layout, err
		}
		owner, err := runtimedata.Acquire(data)
		if err != nil {
			return layout, err
		}
		defer func() { result = errors.Join(result, owner.Release()) }()
		hostGuard, err := instanceguard.Acquire(filepath.Join(data, "server.lock"))
		if err != nil {
			return layout, err
		}
		defer func() { result = errors.Join(result, hostGuard.Release()) }()
		return runtimedata.PrepareStorageOwned(t.Context(), runtimedata.OwnedStorage{Directory: data, Runtime: owner, Host: hostGuard}, time.Now().UTC(), nil)
	}()
	if err != nil {
		t.Fatal(err)
	}
	unavailableRole := filepath.Join(data, layout.Observation.RelativePath)
	if err := os.Mkdir(unavailableRole, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(unavailableRole)
	if err != nil || !before.IsDir() {
		t.Fatalf("optional role fault was not established: %v", err)
	}
	host := startHost(t, options)
	defer shutdownHost(t, host)
	app := exchangeBootstrap(t, host.Bootstrap())
	grant, err := host.Runtime().ManualCaptures().Create(context.Background(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Control survives", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/status", "/api/v1/captures", "/api/v1/environments", "/api/v1/upstream-endpoints", "/api/v1/provider-accounts", "/api/v1/captures/manual_capture:" + grant.Capture.ID + "/native-tools/approvals"} {
		response := controlRequest(t, app.BaseURL, http.MethodGet, path, app.ReadToken, "vibermate://desktop")
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("required dashboard/approval route %s=%d", path, response.StatusCode)
		}
	}
	response := controlRequest(t, app.BaseURL, http.MethodGet, "/api/v1/activities?limit=1", app.ReadToken, "vibermate://desktop")
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("missing mirror advertised history: %d", response.StatusCode)
	}
	if _, err = host.Runtime().StorageLocation(context.Background()); !errors.Is(err, recording.ErrInspectionUnavailable) {
		t.Fatal("unavailable admin stats fabricated", err)
	}
	if !host.Status().Ready {
		t.Fatal("optional inspection refusal disabled host")
	}
	after, err := os.Lstat(unavailableRole)
	if err != nil || !after.IsDir() || !os.SameFile(before, after) {
		t.Fatalf("optional role fault changed during required control work: %v", err)
	}
}

func TestDesktopInternalRuntimeBudgetReleasesGenerationOnLaterRetry(t *testing.T) {
	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	options := hostOptions(t, paths, filepath.Join(root, "data"))
	options.Runtime.Lifecycle.ShutdownTimeout = 40 * time.Millisecond
	host := startHost(t, options)
	deadline := time.Now().Add(5 * time.Second)
	for host.Runtime().RawEvidenceStatistics().WriterID == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if host.Runtime().RawEvidenceStatistics().WriterID == "" {
		t.Fatal("optional writer never ready")
	}
	db, err := sql.Open("sqlite", filepath.Join(options.Runtime.Paths.DataDirectory(), "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE observation_storage_state SET archive_id=archive_id`); err != nil {
		t.Fatal(err)
	}
	stop, cancel := context.WithTimeout(context.Background(), time.Second)
	err = host.Shutdown(stop)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("internal Runtime budget did not expire: %v", err)
	}
	probe, e := instanceguard.Acquire(paths.LockPath())
	if e == nil {
		probe.Release()
		t.Fatal("Desktop generation released before actual Runtime cleanup")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-host.Runtime().ShutdownDone():
	case <-time.After(10 * time.Second):
		t.Fatal("Runtime cleanup did not finish")
	}
	_ = host.Shutdown(context.Background())
	probe, err = instanceguard.Acquire(paths.LockPath())
	if err != nil {
		t.Fatal("completed cleanup retained Desktop generation forever", err)
	}
	probe.Release()
}
