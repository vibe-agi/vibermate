package environment

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
)

// ValidateTransition prevents a stable child identifier from becoming a
// mutable alias and requires every changed child to advance its own revision.
func ValidateTransition(previous, candidate Environment) error {
	prepared, err := materializeIdentityHistory(&previous, candidate)
	if err != nil {
		return err
	}
	if !equalRetiredHistory(prepared.RetiredChildIdentities, candidate.RetiredChildIdentities) {
		return fmt.Errorf("%w: retired child identity history is not materialized", ErrInvalidTransition)
	}
	return validatePreparedTransition(previous, prepared)
}

func validatePreparedTransition(previous, candidate Environment) error {
	oldValue, err := normalize(previous)
	if err != nil {
		return err
	}
	newValue, err := normalize(candidate)
	if err != nil {
		return err
	}
	if oldValue.ID != newValue.ID || newValue.Revision != oldValue.Revision+1 {
		return fmt.Errorf("%w: root identity or revision did not advance exactly once", ErrInvalidTransition)
	}
	oldEndpoints := make(map[ClientEndpointID]ClientEndpoint, len(oldValue.ClientEndpoints))
	oldPlans := make(map[ClientProtocolPlanID]ClientProtocolPlan)
	oldRoutes := make(map[UpstreamRouteID]UpstreamRoute)
	for _, endpoint := range oldValue.ClientEndpoints {
		oldEndpoints[endpoint.ID] = endpoint
		for _, plan := range endpoint.ProtocolPlans {
			oldPlans[plan.ID] = plan
			for _, route := range destinationRoutes(plan.Destination) {
				oldRoutes[route.ID] = route
			}
		}
	}
	for _, endpoint := range newValue.ClientEndpoints {
		old, exists := oldEndpoints[endpoint.ID]
		if !exists {
			if endpoint.Revision != 1 {
				return newChildRevision("ClientEndpoint", endpoint.ID.String())
			}
		} else {
			if old.ClientOrigin != endpoint.ClientOrigin {
				return mutableAlias("ClientEndpoint", endpoint.ID.String())
			}
			if err := requireRevisionChange("ClientEndpoint", endpoint.ID.String(), old.Revision, endpoint.Revision, endpointEqualIgnoringRevision(old, endpoint)); err != nil {
				return err
			}
		}
		for _, plan := range endpoint.ProtocolPlans {
			oldPlan, exists := oldPlans[plan.ID]
			if !exists {
				if plan.Revision != 1 {
					return newChildRevision("ClientProtocolPlan", plan.ID.String())
				}
			} else {
				if oldPlan.ClientProtocol != plan.ClientProtocol {
					return mutableAlias("ClientProtocolPlan", plan.ID.String())
				}
				if err := requireRevisionChange("ClientProtocolPlan", plan.ID.String(), oldPlan.Revision, plan.Revision, planEqualIgnoringRevision(oldPlan, plan)); err != nil {
					return err
				}
			}
			for _, route := range destinationRoutes(plan.Destination) {
				oldRoute, exists := oldRoutes[route.ID]
				if !exists {
					if route.Revision != 1 {
						return newChildRevision("UpstreamRoute", route.ID.String())
					}
				} else {
					if oldRoute.ProviderTarget.ID != route.ProviderTarget.ID ||
						oldRoute.ProviderTarget.RealmID != route.ProviderTarget.RealmID {
						return mutableAlias("UpstreamRoute", route.ID.String())
					}
					if err := requireRevisionChange("UpstreamRoute", route.ID.String(), oldRoute.Revision, route.Revision, routeEqualIgnoringRevision(oldRoute, route)); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func materializeIdentityHistory(previous *Environment, candidate Environment) (Environment, error) {
	if previous == nil {
		if len(candidate.RetiredChildIdentities) != 0 {
			return Environment{}, fmt.Errorf("%w: a new Environment cannot declare retired children", ErrInvalidTransition)
		}
		return normalize(candidate)
	}
	oldValue, err := normalize(*previous)
	if err != nil {
		return Environment{}, err
	}
	if oldValue.ID != candidate.ID || candidate.Revision != oldValue.Revision+1 {
		return Environment{}, fmt.Errorf("%w: root identity or revision did not advance exactly once", ErrInvalidTransition)
	}
	suppliedHistory := slices.Clone(candidate.RetiredChildIdentities)
	prepared := candidate.Clone()
	prepared.RetiredChildIdentities = slices.Clone(oldValue.RetiredChildIdentities)
	oldCurrent := currentChildIdentities(oldValue)
	newCurrent := currentChildIdentities(prepared)
	retired := make(map[string]struct{}, len(oldValue.RetiredChildIdentities))
	for _, identity := range oldValue.RetiredChildIdentities {
		retired[childIdentityKey(identity.Kind, identity.ID)] = struct{}{}
	}
	for key, identity := range newCurrent {
		if _, exists := retired[key]; exists {
			return Environment{}, fmt.Errorf("%w: retired child %q cannot be reused", ErrInvalidTransition, identity.ID)
		}
		if oldIdentity, exists := oldCurrent[key]; exists && oldIdentity.ParentID != identity.ParentID {
			return Environment{}, mutableAlias(string(identity.Kind), identity.ID)
		}
	}
	for key, identity := range oldCurrent {
		if _, exists := newCurrent[key]; exists {
			continue
		}
		identity.RetiredAtRevision = prepared.Revision
		prepared.RetiredChildIdentities = append(prepared.RetiredChildIdentities, identity)
	}
	if len(prepared.RetiredChildIdentities) > MaxRetiredChildIdentities {
		return Environment{}, fmt.Errorf("%w: retired child identity history exceeds its bound", ErrInvalidTransition)
	}
	if len(suppliedHistory) != 0 && !equalRetiredHistory(suppliedHistory, prepared.RetiredChildIdentities) {
		return Environment{}, fmt.Errorf("%w: retired child identity history is Core-owned", ErrInvalidTransition)
	}
	normalized, err := normalize(prepared)
	if err != nil {
		return Environment{}, err
	}
	return normalized, nil
}

func equalRetiredHistory(first, second []RetiredChildIdentity) bool {
	left := slices.Clone(first)
	right := slices.Clone(second)
	sort.Slice(left, func(i, j int) bool {
		return childIdentityKey(left[i].Kind, left[i].ID) < childIdentityKey(left[j].Kind, left[j].ID)
	})
	sort.Slice(right, func(i, j int) bool {
		return childIdentityKey(right[i].Kind, right[i].ID) < childIdentityKey(right[j].Kind, right[j].ID)
	})
	return reflect.DeepEqual(left, right)
}

func requireRevisionChange(label, id string, old, next Revision, equal bool) error {
	if equal && next != old {
		return fmt.Errorf("%w: unchanged %s %q changed revision", ErrInvalidTransition, label, id)
	}
	if !equal && next != old+1 {
		return fmt.Errorf("%w: changed %s %q did not advance exactly once", ErrInvalidTransition, label, id)
	}
	return nil
}

func newChildRevision(label, id string) error {
	return fmt.Errorf("%w: new %s %q must begin at revision 1", ErrInvalidTransition, label, id)
}

func mutableAlias(label, id string) error {
	return fmt.Errorf("%w: %s %q would become a mutable alias", ErrInvalidTransition, label, id)
}

func endpointEqualIgnoringRevision(first, second ClientEndpoint) bool {
	first.Revision, second.Revision = 0, 0
	return reflect.DeepEqual(first, second)
}

func planEqualIgnoringRevision(first, second ClientProtocolPlan) bool {
	first.Revision, second.Revision = 0, 0
	return reflect.DeepEqual(first, second)
}

func routeEqualIgnoringRevision(first, second UpstreamRoute) bool {
	first.Revision, second.Revision = 0, 0
	return reflect.DeepEqual(first, second)
}
