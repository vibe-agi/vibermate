package upstreamservice

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

func TestModelCatalogETagSeparatesAccountAndRouteCaches(t *testing.T) {
	account := providerauth.AccountRef{ID: "account-a", Revision: 1, CredentialEpoch: 1, SettingsRevision: 1, RealmID: "chatgpt", EgressProfile: egressprofile.Direct()}
	account.ResolvedEgress, _ = egressprofile.RestoreResolved(account.EgressProfile, egressprofile.SelectionAccount, 0)
	scope := ModelCatalogScope{EnvironmentID: "env", EnvironmentRevision: 1, RouteID: "route", RouteRevision: 1, EndpointID: "endpoint", EndpointRevision: 1, Origin: "https://chatgpt.com", Account: account}
	first := ModelCatalogETag(`"upstream"`, scope)
	if first == "" || first == `"upstream"` || !strings.HasPrefix(first, `"`) || ModelCatalogETag(`"upstream"`, scope) != first {
		t.Fatalf("unstable or unscoped catalog tag: %q", first)
	}
	for name, change := range map[string]func(*ModelCatalogScope){
		"account":              func(s *ModelCatalogScope) { s.Account.ID = "account-b" },
		"credential":           func(s *ModelCatalogScope) { s.Account.CredentialEpoch++ },
		"settings":             func(s *ModelCatalogScope) { s.Account.SettingsRevision++ },
		"account revision":     func(s *ModelCatalogScope) { s.Account.Revision++ },
		"realm":                func(s *ModelCatalogScope) { s.Account.RealmID = "another-realm" },
		"environment":          func(s *ModelCatalogScope) { s.EnvironmentID = "another-env" },
		"environment revision": func(s *ModelCatalogScope) { s.EnvironmentRevision++ },
		"route":                func(s *ModelCatalogScope) { s.RouteID = "another-route" },
		"mapping revision":     func(s *ModelCatalogScope) { s.RouteRevision++ },
		"endpoint":             func(s *ModelCatalogScope) { s.EndpointID = "another-endpoint" },
		"endpoint revision":    func(s *ModelCatalogScope) { s.EndpointRevision++ },
		"origin":               func(s *ModelCatalogScope) { s.Origin += "/backend-api/codex" },
	} {
		t.Run(name, func(t *testing.T) {
			other := scope
			change(&other)
			if got := ModelCatalogETag(`"upstream"`, other); got == "" || got == first {
				t.Fatalf("scope change reused catalog: %q", got)
			}
		})
	}
	if ModelCatalogETag(`"updated"`, scope) == first {
		t.Fatal("upstream catalog change lost")
	}
	for _, invalid := range []string{"", "bad\r\nheader", strings.Repeat("x", 1025)} {
		if got := ModelCatalogETag(invalid, scope); got != "" {
			t.Fatalf("invalid validator accepted: %q", got)
		}
	}
	scope.Account.CredentialEpoch = 0
	if ModelCatalogETag(`"upstream"`, scope) != "" {
		t.Fatal("invalid account accepted")
	}
}
