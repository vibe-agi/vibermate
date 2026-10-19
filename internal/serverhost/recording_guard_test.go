package serverhost

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func recordingGuardRuntime(t *testing.T) (*productruntime.Runtime, string, *instanceguard.Guard) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "data")
	paths, err := productruntime.NewRuntimePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	root = paths.DataDirectory()
	factory, err := hostsecret.NewServerFileFactory(root)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := productruntime.DefaultLifecycleOptions()
	lifecycle.ShutdownTimeout = 40 * time.Millisecond
	r, err := productruntime.Start(context.Background(), productruntime.Options{Paths: paths, Host: hostcontract.Server(), OfflineHold: gate, Secrets: secrets, Approvals: toolapproval.DefaultConfig(), ExchangeHold: exchange.DefaultHoldPolicy(), Clock: productruntime.SystemClock{}, InstanceIDs: productruntime.NewCryptographicInstanceIDSource(), SecurityRandom: rand.Reader, Lifecycle: lifecycle})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.RawEvidenceStatistics().WriterID == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.RawEvidenceStatistics().WriterID == "" {
		t.Fatal("actual optional writer not ready")
	}
	guard, err := instanceguard.Acquire(filepath.Join(root, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = r.Shutdown(context.Background())
		waitRecordingGuardCleanup(t, r, root)
		_ = guard.Release()
	})
	return r, root, guard
}
func lockRecordingWriter(t *testing.T, root string) func() {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE observation_storage_state SET archive_id=archive_id`); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = tx.Rollback(); _ = db.Close() }) }
	t.Cleanup(release)
	return release
}
func waitRecordingGuardCleanup(t *testing.T, r *productruntime.Runtime, root string) {
	t.Helper()
	select {
	case <-r.ShutdownDone():
		return
	case <-time.After(10 * time.Second):
		t.Fatal("actual Runtime cleanup did not finish")
	}

}
func assertRecordingGuardsHeld(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"runtime-data.lock", "server.lock"} {
		guard, err := instanceguard.Acquire(filepath.Join(root, name))
		if err == nil {
			guard.Release()
			t.Fatalf("released %s while optional writer can still touch files", name)
		}
		if !errors.Is(err, instanceguard.ErrAlreadyOwned) {
			t.Fatal(err)
		}
	}
}
func TestFailedAttachmentRetainsBothGuardsBeyondCleanupBudget(t *testing.T) {
	r, root, guard := recordingGuardRuntime(t)
	release := lockRecordingWriter(t, root)
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	options := DefaultAttachOptions(r, root, productruntime.SystemClock{}, rand.Reader)
	options.Transport = TransportOptions{Mode: TransportHTTP}
	options.ListenAddress = occupied.Addr().String()
	host, attachErr := startAttached(context.Background(), options, guard, true, "", "")
	if attachErr == nil {
		host.Shutdown(context.Background())
		t.Fatal("real occupied attachment succeeded")
	}
	started := time.Now()
	err = rollbackFailedRuntimeStart(r, guard, 50*time.Millisecond, attachErr)
	if err == nil || errors.Is(err, productruntime.ErrInvalidOptions) || time.Since(started) > 500*time.Millisecond {
		t.Fatal("failed bind cleanup was unbounded or lost cause", err)
	}
	assertRecordingGuardsHeld(t, root)
	release()
	waitRecordingGuardCleanup(t, r, root)
	probe, err := instanceguard.Acquire(filepath.Join(root, "server.lock"))
	if err != nil {
		t.Fatal("failed-start handoff retained guard after actual return", err)
	}
	probe.Release()
}
func TestServerInternalRuntimeBudgetCanReleaseGuardOnLaterRetry(t *testing.T) {
	r, root, guard := recordingGuardRuntime(t)
	release := lockRecordingWriter(t, root)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host := &Host{guard: guard, listener: listener, server: &http.Server{}, runtime: r, ownsRuntime: true, shutdown: time.Second, done: make(chan struct{})}
	go host.serve()
	if err = host.Shutdown(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("actual Runtime internal budget did not expire: %v", err)
	}
	assertRecordingGuardsHeld(t, root)
	release()
	waitRecordingGuardCleanup(t, r, root)
	_ = host.Shutdown(context.Background()) // the historical timeout remains reportable
	probe, err := instanceguard.Acquire(filepath.Join(root, "server.lock"))
	if err != nil {
		t.Fatal("actual cleanup completed but historical timeout leaked host guard", err)
	}
	probe.Release()
}
