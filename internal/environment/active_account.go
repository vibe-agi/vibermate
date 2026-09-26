package environment

// ActivateRouteAccount prepares the smallest valid Environment revision that
// changes one manual Route's active Account. Catalog-backed Account references
// are refreshed by the control boundary before this candidate is saved.
func ActivateRouteAccount(
	current Environment,
	routeID UpstreamRouteID,
	account RouteAccountReference,
) (Environment, bool, error) {
	if current.ID == SystemTransparentID || current.Revision >= MaxRevision ||
		validateID("UpstreamRoute ID", routeID.String()) != nil ||
		validateID("ProviderAccount ID", account.ID) != nil ||
		account.Revision == 0 || account.Revision > MaxRevision ||
		!validDisplayName(account.DisplayName) {
		return Environment{}, false, ErrInvalidEnvironment
	}
	candidate := current.Clone()
	for endpointIndex := range candidate.ClientEndpoints {
		endpoint := &candidate.ClientEndpoints[endpointIndex]
		for planIndex := range endpoint.ProtocolPlans {
			plan := &endpoint.ProtocolPlans[planIndex]
			if plan.Destination.Upstream == nil {
				continue
			}
			for routeIndex := range plan.Destination.Upstream.Routes {
				route := &plan.Destination.Upstream.Routes[routeIndex]
				if route.ID != routeID {
					continue
				}
				if route.AccountPolicy.Mode != AccountSelectionFixed ||
					route.AccountPolicy.Revision >= MaxRevision ||
					route.Revision >= MaxRevision || plan.Revision >= MaxRevision ||
					endpoint.Revision >= MaxRevision {
					return Environment{}, false, ErrInvalidTransition
				}
				if route.AccountPolicy.FixedAccountID == account.ID {
					return candidate, false, nil
				}
				candidate.Revision++
				endpoint.Revision++
				plan.Revision++
				route.Revision++
				route.AccountPolicy.Revision++
				route.AccountPolicy.FixedAccountID = account.ID
				route.AccountPolicy.Accounts = []RouteAccountReference{account}
				return candidate, true, nil
			}
		}
	}
	return Environment{}, false, ErrInvalidEnvironment
}
