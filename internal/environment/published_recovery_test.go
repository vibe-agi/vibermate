package environment

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestPublishedRecoveryUsesFrozenReferencesButCandidateAdmissionUsesLiveCatalogs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	value := fixture(t, "published", mustOrigin(t, "https://relay.example"))
	accounts := accountCatalogFor(value)
	compiler := testCompiler(t, accounts)
	repository := newMemoryRepository()
	manager, err := NewManager(ctx, repository, compiler, NewAtomicProjection(), nil)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := manager.SaveDraft(ctx, DraftCommand{Candidate: value})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(ctx, value.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(ctx, preview); err != nil {
		t.Fatal(err)
	}
	before, err := manager.Resolve(value.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"profile upgraded", "account changed", "account removed"} {
		t.Run(scenario, func(t *testing.T) {
			live := accountCatalogFor(value)
			for id, account := range live {
				account.UpstreamEndpointRevision++
				if scenario == "account changed" {
					account.Revision++
					account.DisplayName = "Changed after publication"
					account.Active = false
				}
				live[id] = account
				if scenario == "account removed" {
					delete(live, id)
				}
			}
			current := testCompiler(t, live)
			if _, err := current.Compile(value); !errors.Is(err, ErrInvalidEnvironment) {
				t.Fatalf("stale candidate was admitted: %v", err)
			}
			// Recovery must not even ask for the latest endpoint configuration.
			current.endpoints = unreadableRecoveryEndpoints{}
			recovered, err := NewManager(ctx, repository, current, NewAtomicProjection(), nil)
			if err != nil {
				t.Fatalf("recover after %s: %v", scenario, err)
			}
			for name, read := range map[string]func() (EnvironmentSnapshot, error){
				"projection": func() (EnvironmentSnapshot, error) { return recovered.Resolve(value.ID) },
				"active":     func() (EnvironmentSnapshot, error) { return recovered.Get(ctx, value.ID) },
				"history":    func() (EnvironmentSnapshot, error) { return recovered.GetRevision(ctx, value.ID, 1) },
			} {
				got, err := read()
				if err != nil || got.Digest() != before.Digest() {
					t.Fatalf("%s changed frozen evidence: %v", name, err)
				}
				for _, endpoint := range got.compiled {
					for _, plan := range endpoint.plans {
						for _, route := range plan.upstreamRouteSet.routes {
							for _, account := range route.accountPolicy.Accounts() {
								if account.UpstreamEndpointRevision != 1 || account.Revision != 1 || account.DisplayName != account.ID {
									t.Fatalf("%s used latest account or profile metadata: %+v", name, account)
								}
							}
						}
					}
				}
			}
			listed, err := recovered.List(ctx)
			if err != nil || len(listed) != 2 {
				t.Fatalf("list published policies: count=%d err=%v", len(listed), err)
			}
			next := value.Clone()
			next.Revision++
			next.Name = "New draft"
			if _, err := recovered.SaveDraft(ctx, DraftCommand{ExpectedBaseRevision: 1, Candidate: next}); !errors.Is(err, ErrInvalidEnvironment) {
				t.Fatalf("recovery bypass leaked into draft admission: %v", err)
			}
		})
	}
}

func TestPublishedRecoveryStillRejectsInvalidStructureAndUnexecutablePlans(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Environment){
		"invalid account reference": func(value *Environment) {
			value.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].AccountPolicy.Accounts[0].Revision = 0
		},
		"missing capability": func(value *Environment) {
			value.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].ProviderTarget.Capabilities = nil
		},
		"unavailable wire profile": func(value *Environment) {
			value.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].WireProfileRef = "wire.unavailable"
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := fixture(t, "invalid", mustOrigin(t, "https://relay.example"))
			mutate(&value)
			if _, err := testCompiler(t, nil).Restore(value); err == nil {
				t.Fatal("invalid published state was restored")
			}
		})
	}
}

type unreadableRecoveryEndpoints struct{}

func (unreadableRecoveryEndpoints) LookupEndpoint(string) (upstreamendpoint.Endpoint, bool) {
	panic("published recovery read the current endpoint catalog")
}

func (unreadableRecoveryEndpoints) GuardAccountLink(context.Context, upstreamendpoint.ID, func(upstreamendpoint.Endpoint) error) error {
	panic("published recovery changed an account link")
}
