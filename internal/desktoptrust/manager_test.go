package desktoptrust

import (
	"context"
	"crypto/sha1" // #nosec G505 -- mirrors the macOS trust plist lookup key.
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/systemtrust"
)

type testRootProvider struct {
	identity    localca.RootIdentity
	certificate localca.RootCertificate
}

func (provider testRootProvider) LocalRootIdentity() localca.RootIdentity {
	return provider.identity
}

func (provider testRootProvider) LocalRootCertificate() localca.RootCertificate {
	return provider.certificate
}

type fakeTrustStore struct {
	mu        sync.Mutex
	rootPEM   []byte
	present   bool
	trusted   bool
	mutations int
}

func TestManagerReplaceSchedulesAFreshRootAfterManualRemoval(t *testing.T) {
	manager, _ := newTestManager(t, false, false)
	requested := false
	released := false
	// Rebuild the small manager with a reset callback so the callback is tested
	// through the same public constructor as production.
	owner, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := manager.rootProvider
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager, err := New(Options{
		OwnerContext: owner,
		Root:         provider,
		Executor:     &fakeTrustStore{rootPEM: provider.LocalRootCertificate().CertificatePEM()},
		ResetRequest: func(context.Context, localca.RootIdentity) error {
			requested = true
			return nil
		},
		ReplaceAdmission: func(context.Context) (func(), error) {
			return func() { released = true }, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	result, err := manager.Replace(context.Background())
	if err != nil || result.Completed || !result.RestartRequired || !requested {
		t.Fatalf("replace did not schedule reset: result=%+v error=%v requested=%v", result, err, requested)
	}
	if released {
		t.Fatal("Capture maintenance ended before the required restart")
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("Capture maintenance was not released during shutdown")
	}
}

func TestManagerReplaceRefusesWhenCapturesAreActive(t *testing.T) {
	owner, cancel := context.WithCancel(context.Background())
	defer cancel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	authority, err := localca.Open(context.Background(), localca.DefaultOptions(directory, owner))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authority.Shutdown(context.Background()) })
	store := &fakeTrustStore{
		rootPEM: authority.Certificate().CertificatePEM(),
		present: true,
		trusted: true,
	}
	manager, err := New(Options{
		OwnerContext: owner,
		Root: testRootProvider{
			identity: authority.Identity(), certificate: authority.Certificate(),
		},
		Executor: store,
		ResetRequest: func(context.Context, localca.RootIdentity) error {
			return nil
		},
		ReplaceAdmission: func(context.Context) (func(), error) {
			return nil, ErrRootResetActiveCaptures
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(context.Background())
	if _, err := manager.Replace(context.Background()); !errors.Is(err, ErrRootResetActiveCaptures) {
		t.Fatalf("active capture guard was not enforced: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.present || !store.trusted {
		t.Fatal("replace mutated trust despite active capture")
	}
}

func TestManagerReplaceRequiresTheExactRootToBeRemovedManually(t *testing.T) {
	manager, store := newTestManager(t, true, true)
	requested := false
	released := false
	manager.resetRequest = func(context.Context, localca.RootIdentity) error {
		requested = true
		return nil
	}
	manager.replaceAdmission = func(context.Context) (func(), error) {
		return func() { released = true }, nil
	}

	result, err := manager.Replace(context.Background())
	if !errors.Is(err, ErrRootResetRequiresRemoval) || result.RestartRequired ||
		requested || !released {
		t.Fatalf(
			"installed Root replacement = %+v, error=%v requested=%v released=%v",
			result,
			err,
			requested,
			released,
		)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.mutations != 0 || !store.present || !store.trusted {
		t.Fatalf("replacement mutated current-user trust: %+v", store)
	}
}

func TestConcurrentRootReplacementFailsFastBeforeWaitingForMaintenance(t *testing.T) {
	manager, _ := newTestManager(t, false, false)
	entered := make(chan struct{})
	continueAdmission := make(chan struct{})
	manager.resetRequest = func(context.Context, localca.RootIdentity) error {
		return nil
	}
	manager.replaceAdmission = func(context.Context) (func(), error) {
		close(entered)
		<-continueAdmission
		return func() {}, nil
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.Replace(context.Background())
		firstDone <- err
	}()
	<-entered
	if _, err := manager.Replace(context.Background()); !errors.Is(err, ErrRootResetPending) {
		t.Fatalf("concurrent replacement did not fail fast: %v", err)
	}
	close(continueAdmission)
	if err := <-firstDone; err != nil {
		t.Fatalf("admitted replacement failed: %v", err)
	}
}

func (store *fakeTrustStore) Execute(
	_ context.Context,
	spec systemtrust.CommandSpec,
) (systemtrust.CommandResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	switch spec.Kind() {
	case systemtrust.CommandInspectExactPresence:
		if !store.present {
			return systemtrust.NewCommandResult(
				systemtrust.CommandOutcomeSucceeded, nil, nil,
			)
		}
		return systemtrust.NewCommandResult(
			systemtrust.CommandOutcomeSucceeded, store.rootPEM, nil,
		)
	case systemtrust.CommandInspectUserTrust:
		arguments := spec.Arguments()
		if len(arguments) != 2 {
			return systemtrust.CommandResult{}, systemtrust.ErrCommandInvalid
		}
		trustEntry := ""
		if store.present {
			block, _ := pem.Decode(store.rootPEM)
			if block == nil {
				return systemtrust.CommandResult{}, systemtrust.ErrCommandInvalid
			}
			digest := sha1.Sum(block.Bytes) // #nosec G401 -- macOS lookup key.
			settings := ""
			if store.trusted {
				settings = `<key>trustSettings</key><array><dict>` +
					`<key>kSecTrustSettingsPolicyName</key><string>sslServer</string>` +
					`<key>kSecTrustSettingsResult</key><integer>1</integer>` +
					`</dict></array>`
			}
			trustEntry = `<key>` + strings.ToUpper(hex.EncodeToString(digest[:])) +
				`</key><dict>` + settings + `</dict>`
		}
		output := []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<plist version="1.0"><dict><key>trustList</key><dict>` +
			trustEntry + `</dict></dict></plist>`)
		if err := os.WriteFile(arguments[1], output, 0o600); err != nil {
			return systemtrust.CommandResult{}, err
		}
		return systemtrust.NewCommandResult(
			systemtrust.CommandOutcomeSucceeded, nil, nil,
		)
	case systemtrust.CommandEnsureExactTrust:
		store.mutations++
		store.present = true
		store.trusted = true
	case systemtrust.CommandRemoveExactTrust:
		store.mutations++
		store.trusted = false
	case systemtrust.CommandDeleteExactObject:
		store.mutations++
		store.present = false
		store.trusted = false
	default:
		return systemtrust.CommandResult{}, systemtrust.ErrCommandInvalid
	}
	return systemtrust.NewCommandResult(
		systemtrust.CommandOutcomeSucceeded, nil, nil,
	)
}

func newTestManager(t *testing.T, present, trusted bool) (*Manager, *fakeTrustStore) {
	t.Helper()
	owner, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	authority, err := localca.Open(
		context.Background(),
		localca.DefaultOptions(directory, owner),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = authority.Shutdown(context.Background())
	})
	root := authority.Certificate().CertificatePEM()
	if _, rest := pem.Decode(root); len(rest) != 0 {
		t.Fatal("test certificate is not one PEM block")
	}
	if _, err := x509.ParseCertificate(mustPEMBytes(t, root)); err != nil {
		t.Fatal(err)
	}
	store := &fakeTrustStore{
		rootPEM: root,
		present: present,
		trusted: trusted,
	}
	manager, err := New(Options{
		OwnerContext: owner,
		Root: testRootProvider{
			identity: authority.Identity(), certificate: authority.Certificate(),
		},
		Executor: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	return manager, store
}

func mustPEMBytes(t *testing.T, encoded []byte) []byte {
	t.Helper()
	block, _ := pem.Decode(encoded)
	if block == nil {
		t.Fatal("certificate PEM is invalid")
	}
	return block.Bytes
}

func TestManagerReportsTheExactRootTrust(t *testing.T) {
	manager, _ := newTestManager(t, false, false)
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.CertificatePresent != systemtrust.ExactPresenceAbsent ||
		status.TrustDecision != systemtrust.TrustDecisionUntrusted ||
		!status.RootValid || status.Fingerprint == "" {
		t.Fatalf("unexpected initial status: %+v", status)
	}
}

func TestManagerExportsOnlyExactPublicRootMaterial(t *testing.T) {
	manager, _ := newTestManager(t, false, false)
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	material, err := manager.Material(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(material.CertificateDER)
	if material.RootRevision != status.RootRevision ||
		material.Fingerprint != status.Fingerprint ||
		hex.EncodeToString(digest[:]) != status.Fingerprint {
		t.Fatalf("material=%+v status=%+v", material, status)
	}
	material.CertificateDER[0] ^= 0xff
	again, err := manager.Material(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParseCertificate(again.CertificateDER); err != nil {
		t.Fatalf("caller mutated exported material: %v", err)
	}
}
