package environment

// ActivateRouteAccount prepares the smallest valid Environment revision that
// changes one manual Route's active Account. The control boundary validates the
// selected Account; all other frozen Account references remain unchanged.
func ActivateRouteAccount(
	current Environment,
	routeID UpstreamRouteID,
	account RouteAccountReference,
) (Environment, bool, error) {
	if validateID("ProviderAccount ID", account.ID) != nil ||
		account.Revision == 0 || account.Revision > MaxRevision ||
		!validDisplayName(account.DisplayName) {
		return Environment{}, false, ErrInvalidEnvironment
	}
	return activateRouteAccount(current, routeID, &account)
}

func ActivateRouteOriginalAccount(current Environment, routeID UpstreamRouteID) (Environment, bool, error) {
	return activateRouteAccount(current, routeID, nil)
}

func activateRouteAccount(current Environment, routeID UpstreamRouteID, account *RouteAccountReference) (Environment, bool, error) {
	if current.ID == SystemTransparentID || current.Revision >= MaxRevision || validateID("UpstreamRoute ID", routeID.String()) != nil {
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
				if (route.AccountPolicy.Mode != AccountSelectionFixed && route.AccountPolicy.Mode != AccountSelectionOriginal) ||
					route.AccountPolicy.Revision >= MaxRevision ||
					route.Revision >= MaxRevision || plan.Revision >= MaxRevision ||
					endpoint.Revision >= MaxRevision {
					return Environment{}, false, ErrInvalidTransition
				}
				selectedIndex := -1
				if account == nil {
					if !routePreservesClientAccount(endpoint.ClientOrigin, plan.ClientProtocol, *route) {
						return Environment{}, false, ErrInvalidEnvironment
					}
					if route.AccountPolicy.Mode == AccountSelectionOriginal {
						return candidate, false, nil
					}
				} else {
					for index, selected := range route.AccountPolicy.Accounts {
						if selected.ID == account.ID {
							selectedIndex = index
							break
						}
					}
					if selectedIndex < 0 {
						return Environment{}, false, ErrInvalidEnvironment
					}
					if route.AccountPolicy.Mode == AccountSelectionFixed && route.AccountPolicy.FixedAccountID == account.ID {
						return candidate, false, nil
					}
				}
				candidate.Revision++
				endpoint.Revision++
				plan.Revision++
				route.Revision++
				route.AccountPolicy.Revision++
				route.AllowAccountHistory = false
				route.AccountPolicy.Selector = nil
				if account == nil {
					route.AccountPolicy.Mode = AccountSelectionOriginal
					route.AccountPolicy.FixedAccountID = ""
				} else {
					route.AccountPolicy.Mode = AccountSelectionFixed
					route.AccountPolicy.FixedAccountID = account.ID
					route.AccountPolicy.Accounts[selectedIndex] = *account
				}
				return candidate, true, nil
			}
		}
	}
	return Environment{}, false, ErrInvalidEnvironment
}
