package upstreamendpoint

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

type memoryEndpointRepository struct {
	mu    sync.Mutex
	rows  map[ID]Endpoint
	fails bool
}

func newMemoryEndpointRepository() *memoryEndpointRepository {
	return &memoryEndpointRepository{rows: map[ID]Endpoint{}}
}

func (repository *memoryEndpointRepository) LoadAll(
	context.Context,
) ([]Endpoint, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	items := make([]Endpoint, 0, len(repository.rows))
	for _, endpoint := range repository.rows {
		items = append(items, endpoint.Clone())
	}
	return items, nil
}

func (repository *memoryEndpointRepository) Load(
	_ context.Context,
	id ID,
) (Endpoint, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	endpoint, present := repository.rows[id]
	return endpoint.Clone(), present, nil
}

func (repository *memoryEndpointRepository) Write(
	_ context.Context,
	expected uint64,
	candidate Endpoint,
) (CommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.fails {
		return CommitResult{Outcome: CommitNotCommitted}, nil
	}
	current, present := repository.rows[candidate.ID]
	if expected != 0 && (!present || uint64(current.Revision) != expected) {
		return CommitResult{Outcome: CommitConflict}, nil
	}
	repository.rows[candidate.ID] = candidate.Clone()
	return CommitResult{Outcome: CommitCommitted, Endpoint: candidate.Clone()}, nil
}

type fixedClock struct{}

func (fixedClock) Now() time.Time {
	return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
}

func endpointFixture(t *testing.T) CreateCommand {
	t.Helper()
	origin, err := originidentity.ParseProviderOrigin("https://api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewID("anthropic.primary")
	if err != nil {
		t.Fatal(err)
	}
	driver, err := providerauth.NewDriverRef("anthropic_api_key")
	if err != nil {
		t.Fatal(err)
	}
	return CreateCommand{
		ID:               id,
		DisplayName:      "Anthropic",
		Origin:           origin,
		RealmID:          "anthropic",
		BackendProtocols: []string{"anthropic_messages"},
		Capabilities: []protocolspec.ProviderCapability{
			protocolspec.ProviderCapabilityMessages,
		},
		Drivers: []providerauth.DriverRef{driver},
	}
}

func endpointManager(t *testing.T) (*Manager, *memoryEndpointRepository, ID) {
	t.Helper()
	repository := newMemoryEndpointRepository()
	command := endpointFixture(t)
	manager, err := NewManager(
		context.Background(), repository, []CreateCommand{command}, fixedClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return manager, repository, command.ID
}

func holder(kind resourcedeletion.Kind, id string) resourcedeletion.Holder {
	return resourcedeletion.Holder{Kind: kind, ID: id, Label: "holder " + id}
}

// A published route names this Endpoint. Retiring it underneath would leave
// that route pointing at a target the runtime can no longer resolve.
func TestRetiringAnEndpointIsRefusedWhileARouteNamesIt(t *testing.T) {
	t.Parallel()
	manager, repository, id := endpointManager(t)

	result, err := manager.Delete(
		context.Background(), id,
		func(context.Context, ID) ([]resourcedeletion.Holder, error) {
			return []resourcedeletion.Holder{
				holder(resourcedeletion.KindEnvironmentRoute, "work/route-1"),
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted ||
		len(result.Holders) != 1 ||
		result.Holders[0].Kind != resourcedeletion.KindEnvironmentRoute {
		t.Fatalf("result = %+v, want refused with one route", result)
	}
	repository.mu.Lock()
	state := repository.rows[id].State
	repository.mu.Unlock()
	if state != StateActive {
		t.Fatalf("a refused delete changed state to %q", state)
	}
}

// An Endpoint owns Accounts whose credentials live in the host SecretStore.
// Removing the owner silently would destroy something the user never named.
func TestRetiringAnEndpointIsRefusedWhileItOwnsAnAccount(t *testing.T) {
	t.Parallel()
	manager, _, id := endpointManager(t)

	result, err := manager.Delete(
		context.Background(), id,
		func(context.Context, ID) ([]resourcedeletion.Holder, error) {
			return []resourcedeletion.Holder{
				holder(resourcedeletion.KindOwnedAccount, "account.work"),
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted || result.Holders[0].Kind != resourcedeletion.KindOwnedAccount {
		t.Fatalf("result = %+v, want refused with one owned Account", result)
	}
}

func TestRetiringAnUnheldEndpointRemovesItFromEveryListing(t *testing.T) {
	t.Parallel()
	manager, repository, id := endpointManager(t)

	result, err := manager.Delete(context.Background(), id)
	if err != nil || !result.Deleted {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	listed, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range listed {
		if endpoint.ID == id {
			t.Fatal("a retired Endpoint is still listed")
		}
	}
	if _, present := manager.LookupEndpoint(id.String()); present {
		t.Fatal("a retired Endpoint is still resolvable for new traffic")
	}
	// The row survives, disabled, so evidence frozen against it can still say
	// where a past Exchange went.
	repository.mu.Lock()
	row, kept := repository.rows[id]
	repository.mu.Unlock()
	if !kept || row.State != StateDisabled {
		t.Fatalf("row = %+v, kept = %v; the record should survive as disabled", row, kept)
	}
}

func TestRetiringAnUnknownEndpointIsAnError(t *testing.T) {
	t.Parallel()
	manager, _, _ := endpointManager(t)
	missing, err := NewID("absent.endpoint")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Delete(context.Background(), missing); !errors.Is(
		err, ErrEndpointNotFound,
	) {
		t.Fatalf("Delete() error = %v, want ErrEndpointNotFound", err)
	}
}
