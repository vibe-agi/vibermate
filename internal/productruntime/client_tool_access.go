package productruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"reflect"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

// The immutable default registration is source-pinned to Codex 25af12f,
// ff6aec9 and a956835: core config's ChatGPT default base URL,
// codex-mcp/src/mcp/mod.rs normalize_codex_apps_base_url + /ps/mcp, ChatGpt auth.
// It proves the supported default, not a historical failed request destination.
type clientToolAccessBuilder struct {
	environments environment.Reader
	profiles     egressprofile.Controller
}

// PrepareClientToolAccess prepares permission from the complete unsaved profile.
// No login, credentials, model routing, or network operation is consulted.
func (r *Runtime) PrepareClientToolAccess(ctx context.Context, candidate environment.Environment, requested *environment.ClientToolAccessPolicy) (environment.ClientToolAccessPolicy, error) {
	if r == nil {
		return environment.ClientToolAccessPolicy{}, environment.ErrClientProtocolNotMatched
	}
	return (clientToolAccessBuilder{r.environments, r.egressProfiles}).prepare(ctx, candidate, requested)
}

func (b clientToolAccessBuilder) prepare(ctx context.Context, candidate environment.Environment, requested *environment.ClientToolAccessPolicy) (environment.ClientToolAccessPolicy, error) {
	unsupported := environment.ErrClientProtocolNotMatched
	if ctx == nil || b.environments == nil || requested == nil || !requested.Enabled || candidate.Revision < 1 || candidate.Revision > environment.MaxRevision || candidate.ID == environment.SystemTransparentID {
		return environment.ClientToolAccessPolicy{}, unsupported
	}
	origin, _ := originidentity.ParseProviderOrigin("https://chatgpt.com")
	var selected *egressprofile.ProfileRevision
	for _, endpoint := range candidate.ClientEndpoints {
		if endpoint.ClientOrigin.String() != origin.String() {
			continue
		}
		for _, plan := range endpoint.ProtocolPlans {
			profile := plan.EgressProfile
			if profile.Equal(egressprofile.Direct()) {
				profile = egressprofile.Direct()
			} else {
				if b.profiles == nil {
					return environment.ClientToolAccessPolicy{}, unsupported
				}
				published, err := b.profiles.GetRevision(ctx, profile.ID, profile.Revision)
				if err != nil || !published.Equal(profile) {
					return environment.ClientToolAccessPolicy{}, unsupported
				}
				profile = published
			}
			if profile.Validate() != nil || selected != nil && !selected.Equal(profile) {
				return environment.ClientToolAccessPolicy{}, unsupported
			}
			selected = &profile
		}
	}
	if selected == nil {
		return environment.ClientToolAccessPolicy{}, unsupported
	}
	var previous *environment.ClientToolAccessPolicy
	if candidate.Revision > 1 {
		base, err := b.environments.GetRevision(ctx, candidate.ID, candidate.Revision-1)
		if errors.Is(err, environment.ErrEnvironmentNotFound) {
			return environment.ClientToolAccessPolicy{}, environment.ErrRevisionConflict
		}
		if err != nil {
			return environment.ClientToolAccessPolicy{}, err
		}
		if base.ID() != candidate.ID || base.Revision() != candidate.Revision-1 {
			return environment.ClientToolAccessPolicy{}, environment.ErrInvalidEnvironment
		}
		previous = base.Aggregate().ClientToolAccess
	}
	if requested.Route != nil && (previous == nil || !reflect.DeepEqual(requested.Route, previous.Route)) {
		return environment.ClientToolAccessPolicy{}, unsupported
	}
	route := environment.ClientToolRoute{Revision: 1, TargetOrigin: origin, Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: *selected}
	if previous != nil && previous.Enabled && previous.Route != nil {
		old := previous.Route
		if old.TargetOrigin != route.TargetOrigin || old.Path != route.Path || old.CredentialOwner != route.CredentialOwner || old.Contract != route.Contract {
			return environment.ClientToolAccessPolicy{}, unsupported
		}
		route.ID, route.Revision = old.ID, old.Revision
		if !old.EgressProfile.Equal(route.EgressProfile) {
			if route.Revision == environment.MaxRevision {
				return environment.ClientToolAccessPolicy{}, unsupported
			}
			route.Revision++
		}
	} else {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return environment.ClientToolAccessPolicy{}, err
		}
		route.ID = environment.ClientToolRouteID("client.tools." + hex.EncodeToString(nonce[:]))
	}
	policy := environment.ClientToolAccessPolicy{Enabled: true, Route: &route}
	if err := policy.Validate(); err != nil {
		return environment.ClientToolAccessPolicy{}, unsupported
	}
	return policy, nil
}
