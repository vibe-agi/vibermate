package desktophost

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func TestFailedDesktopStartOwnsRuntimeAndAttachedIOUntilActualReturn(t *testing.T) {
	for _, attached := range []bool{false, true} {
		t.Run(fmt.Sprint("attached=", attached), func(t *testing.T) {
			root := t.TempDir()
			paths, err := NewPaths(filepath.Join(root, "cache"))
			if err != nil {
				t.Fatal(err)
			}
			runtimePaths, err := productruntime.NewRuntimePaths(filepath.Join(root, "data"))
			if err != nil {
				t.Fatal(err)
			}
			factory, err := hostsecret.NewServerFileFactory(runtimePaths.DataDirectory())
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
			opts := DefaultOptions(paths, productruntime.Options{Paths: runtimePaths, Host: hostcontract.Desktop(), SecurityRandom: rand.Reader, Secrets: secrets, OfflineHold: gate, Approvals: toolapproval.DefaultConfig(), ExchangeHold: exchange.DefaultHoldPolicy(), Clock: productruntime.SystemClock{}, InstanceIDs: productruntime.NewCryptographicInstanceIDSource(), Lifecycle: lifecycle})
			opts.ShutdownTimeout = 60 * time.Millisecond
			opts.RemoteServerEnabled = attached
			opts.RemoteServerListenAddress = "127.0.0.1:0"
			occupied, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer occupied.Close()
			opts.ProxyListenAddress = occupied.Addr().String()
			var runtime *productruntime.Runtime
			var remote *serverhost.Host
			var body net.Conn
			var release func()
			var failingAt time.Time
			startRuntime := func(ctx context.Context, options productruntime.Options) (*productruntime.Runtime, error) {
				r, e := productruntime.Start(ctx, options)
				if e != nil {
					return nil, e
				}
				runtime = r
				deadline := time.Now().Add(5 * time.Second)
				for r.RawEvidenceStatistics().WriterID == "" && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if r.RawEvidenceStatistics().WriterID == "" {
					t.Fatal("actual optional writer not ready")
				}
				db, e := sql.Open("sqlite", filepath.Join(runtimePaths.DataDirectory(), "observations.db"))
				if e != nil {
					t.Fatal(e)
				}
				tx, e := db.Begin()
				if e != nil {
					t.Fatal(e)
				}
				if _, e = tx.Exec(`UPDATE observation_storage_state SET archive_id=archive_id`); e != nil {
					t.Fatal(e)
				}
				var once sync.Once
				release = func() { once.Do(func() { _ = tx.Rollback(); _ = db.Close() }) }
				failingAt = time.Now()
				return r, nil
			}
			attachServer := func(ctx context.Context, options serverhost.AttachOptions) (*serverhost.Host, error) {
				h, e := serverhost.Attach(ctx, options)
				if e != nil {
					return nil, e
				}
				remote = h
				body, e = net.DialTimeout("tcp", h.Status().ListenAddress, time.Second)
				if e != nil {
					t.Fatal(e)
				}
				_ = body.SetDeadline(time.Now().Add(5 * time.Second))
				_, e = fmt.Fprintf(body, "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 2\r\nExpect: 100-continue\r\n\r\n", servercontrol.RuntimeUserSessionPath, h.Status().ListenAddress)
				if e != nil {
					t.Fatal(e)
				}
				response, e := http.ReadResponse(bufio.NewReader(body), nil)
				if e != nil {
					t.Fatal(e)
				}
				if response.StatusCode != 100 {
					t.Fatalf("real attached handler never entered body read: %d", response.StatusCode)
				}
				_ = body.SetDeadline(time.Time{})
				failingAt = time.Now()
				return h, nil
			}
			t.Cleanup(func() {
				if body != nil {
					_ = body.Close()
				}
				if release != nil {
					release()
				}
				if runtime != nil {
					_ = runtime.Shutdown(context.Background())
					select {
					case <-runtime.ShutdownDone():
					case <-time.After(10 * time.Second):
						t.Error("actual Runtime cleanup leaked")
					}
				}
				if remote != nil {
					_ = remote.Shutdown(context.Background())
				}
			})
			host, startErr := startWithConstructors(context.Background(), opts, startRuntime, attachServer)
			if host != nil || !errors.Is(startErr, syscall.EADDRINUSE) || !strings.Contains(startErr.Error(), `stage "proxy listener"`) {
				t.Fatalf("lost original post-Runtime bind cause: host=%v error=%v", host, startErr)
			}
			if elapsed := time.Since(failingAt); elapsed > 500*time.Millisecond {
				t.Fatalf("failed-start caller waited for held IO: %s", elapsed)
			}
			assertHeld := func(path string) {
				t.Helper()
				probe, e := instanceguard.Acquire(path)
				if e == nil {
					_ = probe.Release()
					t.Errorf("released %s before actual IO return", path)
				} else if !errors.Is(e, instanceguard.ErrAlreadyOwned) {
					t.Fatal(e)
				}
			}
			assertHeld(paths.LockPath())
			assertHeld(filepath.Join(runtimePaths.DataDirectory(), "runtime-data.lock"))
			if attached {
				assertHeld(filepath.Join(runtimePaths.DataDirectory(), "server.lock"))
			}
			// This must start before the attached handler joins: its body is still held.
			if runtime.Status().State != productruntime.RuntimeStateStopping && runtime.Status().State != productruntime.RuntimeStateStopped && runtime.Status().State != productruntime.RuntimeStateStopFailed {
				t.Errorf("Runtime cancellation not initiated before attached handler join: %s", runtime.Status().State)
			}
			second, e := Start(context.Background(), opts)
			if second != nil {
				_ = second.Shutdown(context.Background())
			}
			if !errors.Is(e, instanceguard.ErrAlreadyOwned) || !strings.Contains(e.Error(), "Desktop generation") {
				t.Errorf("second Desktop generation admitted during failed cleanup: %v", e)
			}
			release()
			select {
			case <-runtime.ShutdownDone():
			case <-time.After(10 * time.Second):
				t.Fatal("Runtime did not finish while attached body remained held")
			}
			if attached {
				assertHeld(paths.LockPath())
				assertHeld(filepath.Join(runtimePaths.DataDirectory(), "server.lock"))
				_ = body.Close()
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				probe, e := instanceguard.Acquire(paths.LockPath())
				if e == nil {
					_ = probe.Release()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("failed-start owner leaked generation after actual returns", e)
				}
				time.Sleep(time.Millisecond)
			}
			if attached && !remote.ShutdownCompleted() {
				t.Fatal("Desktop released before attached handler cleanup")
			}
		})
	}
}
