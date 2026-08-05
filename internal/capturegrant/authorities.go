package capturegrant

import (
	"context"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
	"github.com/vibe-agi/vibermate/internal/workspaceroute"
)

// CaptureAuthoritySet freezes the two different launcher decisions made for
// one workspace. Protected authorities may receive the local Root and be
// removed from NO_PROXY. Only the managed subset may have client login inputs
// replaced with a local placeholder.
type CaptureAuthoritySet struct {
	protected []string
	managed   []string
}

func NewCaptureAuthoritySet(
	protected []string,
	managed []string,
) (CaptureAuthoritySet, error) {
	protectedCopy, protectedSet, err := canonicalAuthorities(protected)
	if err != nil {
		return CaptureAuthoritySet{}, err
	}
	managedCopy, _, err := canonicalAuthorities(managed)
	if err != nil {
		return CaptureAuthoritySet{}, err
	}
	for _, authority := range managedCopy {
		if _, exists := protectedSet[authority]; !exists {
			return CaptureAuthoritySet{}, errors.New(
				"managed credential authority is not protected",
			)
		}
	}
	return CaptureAuthoritySet{
		protected: protectedCopy,
		managed:   managedCopy,
	}, nil
}

func (set CaptureAuthoritySet) ProtectedAuthorities() []string {
	return append([]string(nil), set.protected...)
}

func (set CaptureAuthoritySet) ManagedCredentialAuthorities() []string {
	return append([]string(nil), set.managed...)
}

type CaptureAuthorityResolver interface {
	ResolveCaptureAuthorities(
		context.Context,
		workspaceidentity.Scope,
	) (CaptureAuthoritySet, error)
}

type routeAwareAuthorityResolver struct {
	plans  access.ActivePlanCatalog
	routes workspaceroute.Resolver
}

func NewRouteAwareAuthorityResolver(
	plans access.ActivePlanCatalog,
	routes workspaceroute.Resolver,
) (CaptureAuthorityResolver, error) {
	if plans == nil || routes == nil {
		return nil, errors.New("CaptureRun route authority dependencies are incomplete")
	}
	return &routeAwareAuthorityResolver{plans: plans, routes: routes}, nil
}

func (resolver *routeAwareAuthorityResolver) ResolveCaptureAuthorities(
	ctx context.Context,
	scope workspaceidentity.Scope,
) (CaptureAuthoritySet, error) {
	if resolver == nil || resolver.plans == nil || resolver.routes == nil || ctx == nil {
		return CaptureAuthoritySet{}, errors.New("CaptureRun route authority is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return CaptureAuthoritySet{}, err
	}
	if scope != (workspaceidentity.Scope{}) && scope.Validate() != nil {
		return CaptureAuthoritySet{}, errors.New("CaptureRun workspace scope is invalid")
	}
	plans, err := resolver.plans.ActiveAccessPlans()
	if err != nil {
		return CaptureAuthoritySet{}, err
	}
	protected := make([]string, 0, len(plans))
	managed := make([]string, 0, len(plans))
	for _, plan := range plans {
		profileID, profileKind, resolveErr := resolver.resolveProfile(
			ctx,
			plan,
			scope,
		)
		if resolveErr != nil {
			return CaptureAuthoritySet{}, resolveErr
		}
		if profileID.String() == "" {
			return CaptureAuthoritySet{}, errors.New("CaptureRun route profile is unavailable")
		}
		authority := plan.AgentEndpoint().ClientOrigin.EndpointAuthority()
		protected = append(protected, authority)
		if profileKind == access.EndpointProfileManaged {
			managed = append(managed, authority)
		}
	}
	return NewCaptureAuthoritySet(protected, managed)
}

func (resolver *routeAwareAuthorityResolver) resolveProfile(
	ctx context.Context,
	plan access.AccessPlanSnapshot,
	scope workspaceidentity.Scope,
) (access.EndpointProfileID, access.EndpointProfileKind, error) {
	if scope == (workspaceidentity.Scope{}) {
		candidate, found := plan.Candidate(0)
		if !found {
			return access.EndpointProfileID{}, "", errors.New(
				"CaptureRun default route has no candidate",
			)
		}
		return candidate.ProfileID(), candidate.Kind(), nil
	}
	resolution, err := resolver.routes.Resolve(ctx, plan, scope)
	if err != nil {
		return access.EndpointProfileID{}, "", err
	}
	defer resolution.Release()
	for index := 0; index < plan.CandidateCount(); index++ {
		candidate, found := plan.Candidate(index)
		if found && candidate.ProfileID() == resolution.ProfileID {
			return candidate.ProfileID(), candidate.Kind(), nil
		}
	}
	return access.EndpointProfileID{}, "", errors.New(
		"CaptureRun workspace route is outside the active plan",
	)
}

func canonicalAuthorities(
	values []string,
) ([]string, map[string]struct{}, error) {
	copyValues := append([]string(nil), values...)
	seen := make(map[string]struct{}, len(copyValues))
	for _, authority := range copyValues {
		host, port, err := net.SplitHostPort(authority)
		if err != nil || host == "" || strings.ToLower(host) != host ||
			strings.TrimSpace(authority) != authority {
			return nil, nil, errors.New("CaptureRun authority is invalid")
		}
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return nil, nil, errors.New("CaptureRun authority port is invalid")
		}
		if _, duplicate := seen[authority]; duplicate {
			return nil, nil, errors.New("CaptureRun authority is duplicated")
		}
		seen[authority] = struct{}{}
	}
	sort.Strings(copyValues)
	return copyValues, seen, nil
}
