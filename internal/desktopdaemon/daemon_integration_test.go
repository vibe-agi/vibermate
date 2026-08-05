package desktopdaemon_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
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
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

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
				Secrets:        unavailableSecrets{},
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
	store := unavailableSecrets{}
	factory := &secretFactoryFixture{store: store}
	options, err := desktopdaemon.ProductionOptions(
		context.Background(),
		filepath.Join(root, "cache"),
		filepath.Join(root, "data"),
		"tauri://localhost",
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
		got[0] != "tauri://localhost" {
		t.Fatalf("allowed origins = %v", got)
	}

	if _, err := desktopdaemon.ProductionOptions(
		context.Background(),
		filepath.Join(root, "other-cache"),
		filepath.Join(root, "other-data"),
		"tauri://localhost",
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

type unavailableSecrets struct{}

func (unavailableSecrets) Read(
	context.Context,
	secretstore.Reference,
) (*secretstore.Value, error) {
	return nil, secretstore.ErrNotFound
}

func (unavailableSecrets) Inspect(
	context.Context,
	secretstore.Reference,
) (secretstore.Metadata, error) {
	return secretstore.Metadata{State: secretstore.StateMissing}, nil
}

func (unavailableSecrets) Replace(
	context.Context,
	secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrReadOnly
}

func (unavailableSecrets) Delete(
	context.Context,
	secretstore.Reference,
) error {
	return secretstore.ErrNotFound
}
