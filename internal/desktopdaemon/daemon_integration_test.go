package desktopdaemon_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/desktopdaemon"
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/serverdaemon"
	"github.com/vibe-agi/vibermate/internal/serverhost"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func TestDaemonsPreserveUnsupportedDatabaseInsteadOfStartingEmpty(t *testing.T) {
	for _, mode := range []string{"desktop", "server"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			paths, err := productruntime.NewRuntimePaths(root)
			if err != nil {
				t.Fatal(err)
			}
			database, err := sql.Open("sqlite", paths.DatabasePath())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TABLE old_data(value TEXT); INSERT INTO old_data VALUES ('keep me')`); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(paths.DatabasePath())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			factory := &secretFactoryFixture{store: seededSecrets{}}
			if mode == "desktop" {
				options, optionErr := desktopdaemon.ProductionOptions(ctx, filepath.Join(root, "cache"), root, "vibermate://desktop", io.Discard, factory)
				if optionErr != nil {
					t.Fatal(optionErr)
				}
				err = desktopdaemon.Run(ctx, options)
			} else {
				options, optionErr := serverdaemon.ProductionOptions(ctx, root, "127.0.0.1:0", "", serverhost.TransportOptions{Mode: serverhost.TransportSelfSignedTLS}, factory, io.Discard)
				if optionErr != nil {
					t.Fatal(optionErr)
				}
				err = serverdaemon.Run(ctx, options)
			}
			if !errors.Is(err, runtimepersistence.ErrSchemaBaselineMismatch) {
				t.Fatalf("startup error = %v, want unsupported schema", err)
			}
			after, err := os.Stat(paths.DatabasePath())
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("startup replaced the database: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "development-backups")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("startup silently archived user data: %v", err)
			}
			database, err = sql.Open("sqlite", paths.DatabasePath())
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			var value string
			if err := database.QueryRow(`SELECT value FROM old_data`).Scan(&value); err != nil || value != "keep me" {
				t.Fatalf("original data unavailable: value=%q err=%v", value, err)
			}
		})
	}
}

func TestDaemonPublishesBootstrapThenParentEOFReleasesGeneration(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	hostPaths, err := desktophost.NewPaths(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	runtimePaths, err := productruntime.NewRuntimePaths(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	bootstrapReader, bootstrapWriter := io.Pipe()
	parentLifetime, parentOwner := io.Pipe()
	defer bootstrapReader.Close()
	defer bootstrapWriter.Close()
	defer parentOwner.Close()
	parentOwnership, err := desktopdaemon.NewParentOwnership(
		context.Background(),
		parentLifetime,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer parentOwnership.Close()
	done := make(chan error, 1)
	go func() {
		done <- desktopdaemon.Run(parentOwnership.Context(), desktopdaemon.Options{
			Host: desktophost.DefaultOptions(hostPaths, productruntime.Options{
				Paths:          runtimePaths,
				Host:           hostcontract.Desktop(),
				OfflineHold:    gate,
				Secrets:        seededSecrets{},
				Approvals:      toolapproval.DefaultConfig(),
				ExchangeHold:   exchange.DefaultHoldPolicy(),
				Clock:          productruntime.SystemClock{},
				InstanceIDs:    productruntime.NewCryptographicInstanceIDSource(),
				SecurityRandom: rand.Reader,
				Lifecycle:      productruntime.DefaultLifecycleOptions(),
			}),
			BootstrapWriter: bootstrapWriter,
			ShutdownTimeout: 20 * time.Second,
		})
	}()
	decoder := json.NewDecoder(bootstrapReader)
	var progress desktopbootstrap.Progress
	if err := decoder.Decode(&progress); err != nil {
		t.Fatal(err)
	}
	if err := progress.Validate(); err != nil {
		t.Fatalf("bootstrap progress = %+v: %v", progress, err)
	}
	var descriptor desktopbootstrap.Descriptor
	if err := decoder.Decode(&descriptor); err != nil {
		t.Fatal(err)
	}
	if descriptor.Schema != desktopbootstrap.DescriptorSchema ||
		descriptor.BaseURL == "" ||
		descriptor.BootstrapNonce == "" {
		t.Fatalf("bootstrap descriptor = %+v", descriptor)
	}
	if err := parentOwner.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Desktop daemon did not converge after parent EOF")
	}
	guard, err := instanceguard.Acquire(hostPaths.LockPath())
	if err != nil {
		t.Fatalf("generation lock remained owned after parent EOF: %v", err)
	}
	if err := guard.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestProductionOptionsUsesExplicitSecretStoreFactory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := seededSecrets{}
	factory := &secretFactoryFixture{store: store}
	options, err := desktopdaemon.ProductionOptions(
		context.Background(),
		filepath.Join(root, "cache"),
		filepath.Join(root, "data"),
		"vibermate://desktop",
		io.Discard,
		factory,
	)
	if err != nil {
		t.Fatal(err)
	}
	if factory.opens != 1 || options.Host.Runtime.Secrets != store {
		t.Fatalf(
			"factory opens=%d runtime store=%T",
			factory.opens,
			options.Host.Runtime.Secrets,
		)
	}
	if got := options.Host.AllowedOrigins; len(got) != 1 ||
		got[0] != "vibermate://desktop" {
		t.Fatalf("allowed origins = %v", got)
	}
	if options.Host.RemoteServerListenAddress != "127.0.0.1:9666" {
		t.Fatalf(
			"default remote Server listen address = %q, want loopback",
			options.Host.RemoteServerListenAddress,
		)
	}

	if _, err := desktopdaemon.ProductionOptions(
		context.Background(),
		filepath.Join(root, "other-cache"),
		filepath.Join(root, "other-data"),
		"vibermate://desktop",
		io.Discard,
		&secretFactoryFixture{},
	); err == nil {
		t.Fatal("nil Store result was accepted")
	}
	if _, err := desktopdaemon.ProductionOptions(
		context.Background(),
		filepath.Join(root, "invalid-origin-cache"),
		filepath.Join(root, "invalid-origin-data"),
		"https://example.com",
		io.Discard,
		factory,
	); err == nil {
		t.Fatal("unsupported Webview origin was accepted")
	}
}

func TestProductionOptionsRejectsRetiredDesktopOrigins(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, origin := range []string{"tauri://localhost", "http://127.0.0.1:1420"} {
		if _, err := desktopdaemon.ProductionOptions(
			context.Background(),
			filepath.Join(root, "cache"),
			filepath.Join(root, "data"),
			origin,
			io.Discard,
			&secretFactoryFixture{store: seededSecrets{}},
		); err == nil {
			t.Fatalf("retired Desktop origin %q was accepted", origin)
		}
	}
}

type secretFactoryFixture struct {
	store secretstore.Store
	opens int
}

func (factory *secretFactoryFixture) Open(
	context.Context,
) (secretstore.Store, error) {
	factory.opens++
	return factory.store, nil
}

type seededSecrets struct{}

func (seededSecrets) Read(
	_ context.Context,
	reference secretstore.Reference,
) (*secretstore.Value, error) {
	if reference.String() == "secret://runtime/client-annotation-signing-v1" {
		return secretstore.NewValue([]byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"))
	}
	return nil, secretstore.ErrNotFound
}

func (seededSecrets) ReadAtRevision(
	ctx context.Context,
	reference secretstore.Reference,
	revision secretstore.Revision,
) (*secretstore.Value, error) {
	if reference.String() == "secret://runtime/client-annotation-signing-v1" && revision != 1 {
		return nil, secretstore.ErrRevisionConflict
	}
	return seededSecrets{}.Read(ctx, reference)
}

func (seededSecrets) Inspect(
	_ context.Context,
	reference secretstore.Reference,
) (secretstore.Metadata, error) {
	if reference.String() == "secret://runtime/client-annotation-signing-v1" {
		return secretstore.Metadata{State: secretstore.StateConfigured, Revision: 1}, nil
	}
	return secretstore.Metadata{State: secretstore.StateMissing}, nil
}

func (seededSecrets) Replace(
	context.Context,
	secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrReadOnly
}

func (seededSecrets) Delete(
	_ context.Context,
	reference secretstore.Reference,
) error {
	if reference.String() == "secret://runtime/client-annotation-signing-v1" {
		return secretstore.ErrReadOnly
	}
	return secretstore.ErrNotFound
}
