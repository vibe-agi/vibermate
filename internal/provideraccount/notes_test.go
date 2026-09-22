package provideraccount

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type heldNoteRepository struct {
	*memoryRepository
	started chan struct{}
	resume  chan struct{}
}

func (repository *heldNoteRepository) WriteNote(ctx context.Context, expected uint64, candidate Account) (CommitResult, error) {
	close(repository.started)
	select {
	case <-repository.resume:
		return repository.memoryRepository.WriteNote(ctx, expected, candidate)
	case <-ctx.Done():
		return CommitResult{Outcome: CommitNotCommitted}, ctx.Err()
	}
}

func TestNotePersistenceDoesNotBlockNewUpstreamRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repository := &heldNoteRepository{memoryRepository: &memoryRepository{accounts: map[ID]Account{}}, started: make(chan struct{}), resume: make(chan struct{})}
	var once sync.Once
	resume := func() { once.Do(func() { close(repository.resume) }) }
	defer resume()
	endpoints := testEndpoints(t)
	manager, err := NewManager(ctx, repository, newMemorySecrets(), endpoints, BuiltInRealms(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(ctx) })
	secret, err := newTestCredentialValue(t, "note-live-request-fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	if _, err := manager.Create(ctx, CreateCommand{ID: "noted", DisplayName: "Work", UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID, Driver: providerauth.AnthropicAPIKeyDriverRef(), Secret: secret}); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		_, err := manager.SetNote(ctx, NoteCommand{ID: "noted", Note: "during traffic"})
		completed <- err
	}()
	select {
	case <-repository.started:
	case <-ctx.Done():
		t.Fatal("note did not reach persistence")
	}
	lease, err := manager.AcquireEndpointCredential(ctx, "noted", endpoints[upstreamendpoint.AnthropicOfficialID])
	if err != nil {
		t.Fatalf("note interrupted traffic: %v", err)
	}
	lease.Release()
	resume()
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
}

func TestAccountNoteIsIndependentOfRoutingAndCredentials(t *testing.T) {
	ctx := context.Background()
	endpoints := testEndpoints(t)
	repository := &memoryRepository{accounts: map[ID]Account{}}
	secrets := newMemorySecrets()
	manager, err := NewManager(ctx, repository, secrets, endpoints, BuiltInRealms(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(ctx) })
	secret, err := newTestCredentialValue(t, "note-fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	before, err := manager.Create(ctx, CreateCommand{ID: "noted", DisplayName: "Work", UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID, Driver: providerauth.AnthropicAPIKeyDriverRef(), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, _ := manager.LookupAccount("noted", upstreamendpoint.AnthropicOfficialID.String())
	lease, err := manager.AcquireEndpointCredential(ctx, "noted", endpoints[upstreamendpoint.AnthropicOfficialID])
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	updated, err := manager.SetNote(ctx, NoteCommand{ID: "noted", Note: "  \u56e2\u961f\u65e5\u5e38 · \u7814\u53d1  "})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Account.Note != "\u56e2\u961f\u65e5\u5e38 · \u7814\u53d1" || updated.Account.NoteRevision != 1 || updated.Health != before.Health || updated.Account.SecretRef != before.Account.SecretRef || updated.Account.Associations != before.Account.Associations || updated.Account.AssociationRevision != before.Account.AssociationRevision || updated.Account.Revision != before.Account.Revision || updated.Account.DisplayName != before.Account.DisplayName {
		t.Fatalf("note changed account authority: %+v", updated)
	}
	after, found := manager.LookupAccount("noted", upstreamendpoint.AnthropicOfficialID.String())
	if !found || after.Revision != descriptor.Revision || after.DisplayName != descriptor.DisplayName {
		t.Fatal("note invalidated frozen routing")
	}
	if len(secrets.values) != 1 {
		t.Fatal("note copied credentials")
	}
	if _, err := manager.SetNote(ctx, NoteCommand{ID: "noted", Note: "stale"}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update: %v", err)
	}
	unchanged, err := manager.SetNote(ctx, NoteCommand{ID: "noted", ExpectedRevision: 1, Note: updated.Account.Note})
	if err != nil || unchanged.Account.NoteRevision != 1 {
		t.Fatal("no-op changed revision")
	}
	cleared, err := manager.SetNote(ctx, NoteCommand{ID: "noted", ExpectedRevision: 1, Note: " "})
	if err != nil || cleared.Account.Note != "" || cleared.Account.NoteRevision != 2 {
		t.Fatalf("clear: %+v %v", cleared, err)
	}
	for _, invalid := range []string{strings.Repeat("\u5907", MaxNoteCharacters+1), "bad\nline", "bad\x00note", string([]byte{0xff})} {
		if _, err := manager.SetNote(ctx, NoteCommand{ID: "noted", ExpectedRevision: 2, Note: invalid}); !errors.Is(err, ErrInvalidAccount) {
			t.Fatalf("accepted invalid note: %v", err)
		}
	}
}
