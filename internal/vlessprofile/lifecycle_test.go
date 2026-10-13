package vlessprofile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// Leaving startup on the caller context makes Close hang at either private
// Store access or node bootstrap even while that caller remains live.
func TestCloseCancelsAndJoinsPendingStartup(t *testing.T) {
	for _, boundary := range []string{"store", "resolver"} {
		t.Run(boundary, func(t *testing.T) {
			entered := make(chan struct{})
			finished := make(chan struct{})
			store := &lifecycleStore{block: boundary == "store", entered: entered, finished: finished}
			resolver := &lifecycleResolver{entered: entered, finished: finished}
			manager := lifecycleManager(t, store, resolver)
			caller, cancelCaller := context.WithCancel(context.Background())
			dialed := make(chan error, 1)
			closed := make(chan error, 1)
			dialJoined, closeJoined, closeStarted := false, false, false
			go func() {
				conn, err := manager.DialContext(caller, lifecyclePolicy(), "tcp", "127.0.0.1:80")
				if conn != nil {
					conn.Close()
				}
				dialed <- err
			}()
			// Always unblock and join the original failing implementation too.
			defer func() {
				cancelCaller()
				if !closeStarted {
					go func() { closed <- manager.Close() }()
				}
				if !dialJoined {
					awaitLifecycle(t, dialed, "caller startup cleanup")
				}
				if !closeJoined {
					awaitLifecycle(t, closed, "Close cleanup")
				}
			}()
			awaitLifecycle(t, entered, "startup entry")
			go func() { closed <- manager.Close() }()
			closeStarted = true
			watch, cancelWatch := context.WithTimeout(t.Context(), time.Second)
			defer cancelWatch()
			select {
			case <-finished:
			case <-watch.Done():
				t.Fatal("manager Close did not cancel its pending " + boundary + " startup")
			}
			if caller.Err() != nil {
				t.Fatal("manager shutdown canceled the independent caller")
			}
			err := awaitLifecycle(t, closed, "Close join")
			closeJoined = true
			if err != nil {
				t.Fatal(err)
			}
			err = awaitLifecycle(t, dialed, "canceled startup join")
			dialJoined = true
			if err == nil {
				t.Fatal("shutdown startup unexpectedly returned a connection")
			}
			conn, err := manager.DialContext(caller, lifecyclePolicy(), "tcp", "127.0.0.1:80")
			if conn != nil {
				conn.Close()
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Fatal("closed manager accepted another startup")
			}
		})
	}
}

// Adding manager ownership must not leave the caller unable to cancel Store
// access while the manager itself is still live.
func TestCallerCancellationStillJoinsStartup(t *testing.T) {
	entered, finished := make(chan struct{}), make(chan struct{})
	manager := lifecycleManager(t, &lifecycleStore{block: true, entered: entered, finished: finished}, nil)
	caller, cancelCaller := context.WithCancel(context.Background())
	dialed := make(chan error, 1)
	go func() { _, err := manager.DialContext(caller, lifecyclePolicy(), "tcp", "127.0.0.1:80"); dialed <- err }()
	joined := false
	defer func() {
		cancelCaller()
		if !joined {
			awaitLifecycle(t, dialed, "caller cancellation cleanup")
		}
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	}()
	awaitLifecycle(t, entered, "caller cancellation startup entry")
	cancelCaller()
	awaitLifecycle(t, finished, "caller cancellation observed by Store")
	err := awaitLifecycle(t, dialed, "caller cancellation joined")
	joined = true
	if err == nil {
		t.Fatal("caller cancellation returned a connection")
	}
}

// A startup dependency may acknowledge cancellation before finishing its
// cleanup. Shutdown must honor its own context without reopening admission.
func TestShutdownContextBoundsPendingStartupDrain(t *testing.T) {
	entered, canceled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	store := &lifecycleStore{block: true, entered: entered, canceled: canceled, finished: finished, release: release}
	manager := lifecycleManager(t, store, nil)
	shutdown, ok := any(manager).(interface{ Shutdown(context.Context) error })
	if !ok {
		t.Fatal("manager has no context-bounded shutdown")
	}
	caller, cancelCaller := context.WithCancel(context.Background())
	dialed := make(chan error, 1)
	go func() { _, err := manager.DialContext(caller, lifecyclePolicy(), "tcp", "127.0.0.1:80"); dialed <- err }()
	defer func() {
		close(release)
		cancelCaller()
		awaitLifecycle(t, dialed, "bounded startup cleanup")
		if err := shutdown.Shutdown(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	awaitLifecycle(t, entered, "bounded startup entry")
	shutdownContext, cancelShutdown := context.WithCancel(context.Background())
	defer cancelShutdown()
	returned := make(chan error, 1)
	go func() { returned <- shutdown.Shutdown(shutdownContext) }()
	awaitLifecycle(t, canceled, "manager cancellation before delayed cleanup")
	cancelShutdown()
	if err := awaitLifecycle(t, returned, "bounded shutdown return"); !errors.Is(err, context.Canceled) {
		t.Fatalf("bounded shutdown returned %v", err)
	}
	if caller.Err() != nil {
		t.Fatal("bounded shutdown canceled independent caller")
	}
	if _, err := manager.DialContext(caller, lifecyclePolicy(), "tcp", "127.0.0.1:80"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("expired shutdown wait reopened startup admission")
	}
}

func awaitLifecycle[T any](t *testing.T, result <-chan T, event string) T {
	t.Helper()
	watch, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case value := <-result:
		return value
	case <-watch.Done():
		t.Fatal("missing lifecycle event: " + event)
		var zero T
		return zero
	}
}

func lifecyclePolicy() egressnetwork.ProxyPolicy {
	return egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyVLESS, CredentialRef: "secret://egress-vless/lifecycle", CredentialRevision: 1}
}

func lifecycleManager(t *testing.T, store secretstore.Store, resolver egressnetwork.Resolver) *Manager {
	t.Helper()
	profiles, err := egressprofile.NewManager(lifecycleRepository{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(Options{Store: store, Profiles: profiles, Owner: context.Background(), Executable: "/owned/unused-xray", SystemResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

type lifecycleRepository struct{}

func (lifecycleRepository) Write(context.Context, egressprofile.Revision, egressprofile.ProfileRevision) (egressprofile.CommitResult, error) {
	return egressprofile.CommitResult{}, egressprofile.ErrWriteNotCommitted
}
func (lifecycleRepository) LoadRevision(context.Context, egressprofile.ID, egressprofile.Revision) (egressprofile.ProfileRevision, bool, error) {
	return egressprofile.ProfileRevision{}, false, nil
}
func (lifecycleRepository) LoadCurrent(context.Context) ([]egressprofile.ProfileRevision, error) {
	return nil, nil
}

type lifecycleStore struct {
	block    bool
	entered  chan struct{}
	finished chan struct{}
	canceled chan struct{}
	release  <-chan struct{}
}

func (s *lifecycleStore) Read(ctx context.Context, ref secretstore.Reference) (*secretstore.Value, error) {
	return s.ReadAtRevision(ctx, ref, 1)
}
func (s *lifecycleStore) ReadAtRevision(ctx context.Context, ref secretstore.Reference, revision secretstore.Revision) (*secretstore.Value, error) {
	if ref.String() != "secret://egress-vless/lifecycle" || revision != 1 {
		return nil, secretstore.ErrNotFound
	}
	if s.block {
		close(s.entered)
		<-ctx.Done()
		if s.canceled != nil {
			close(s.canceled)
		}
		if s.release != nil {
			<-s.release
		}
		close(s.finished)
		return nil, ctx.Err()
	}
	data, err := json.Marshal(payload{Host: "node.lifecycle.fixture", Port: 443, UUID: "11111111-2222-4333-8444-555555555555", SNI: "node.lifecycle.fixture", Key: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	if err != nil {
		return nil, err
	}
	defer clear(data)
	return secretstore.NewValue(data)
}
func (*lifecycleStore) Inspect(context.Context, secretstore.Reference) (secretstore.Metadata, error) {
	return secretstore.Metadata{State: secretstore.StateConfigured, Revision: 1}, nil
}
func (*lifecycleStore) Replace(context.Context, secretstore.ReplaceCommand) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrReadOnly
}
func (*lifecycleStore) Delete(context.Context, secretstore.Reference) error {
	return secretstore.ErrReadOnly
}

type lifecycleResolver struct {
	entered  chan struct{}
	finished chan struct{}
}

func (r *lifecycleResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" || host != "node.lifecycle.fixture" {
		return nil, errors.New("unexpected bootstrap target")
	}
	close(r.entered)
	<-ctx.Done()
	close(r.finished)
	return nil, ctx.Err()
}
