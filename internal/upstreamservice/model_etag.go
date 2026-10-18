package upstreamservice

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/vibe-agi/vibermate/internal/providerauth"
)

// ModelCatalogScope gives both the catalog and inference response the same
// cache namespace. Codex cannot observe managed account changes in its own auth
// manager, so the upstream validator alone cannot identify its visible catalog.
type ModelCatalogScope struct {
	EnvironmentID       string
	EnvironmentRevision uint64
	RouteID             string
	RouteRevision       uint64
	EndpointID          string
	EndpointRevision    uint64
	Origin              string
	Account             providerauth.AccountRef
}

// ModelCatalogETag preserves upstream invalidation while isolating account,
// credential and model-mapping revisions. Never invent a validator when the
// upstream omitted one: doing so could keep a stale catalog fresh indefinitely.
func ModelCatalogETag(upstream string, scope ModelCatalogScope) string {
	if !validModelValidator(upstream) || scope.Account.Validate() != nil ||
		scope.EnvironmentID == "" || scope.EnvironmentRevision == 0 ||
		scope.RouteID == "" || scope.RouteRevision == 0 || scope.EndpointID == "" ||
		scope.EndpointRevision == 0 || scope.Origin == "" {
		return ""
	}
	account := scope.Account
	identity, _ := json.Marshal([]any{upstream, scope.EnvironmentID, scope.EnvironmentRevision,
		scope.RouteID, scope.RouteRevision, scope.EndpointID, scope.EndpointRevision, scope.Origin,
		account.ID, account.Revision, account.CredentialEpoch, account.SettingsRevision, account.RealmID})
	return fmt.Sprintf(`"vibermate-models-v1-%x"`, sha256.Sum256(identity))
}

// ModelValidator accepts exactly one bounded visible ASCII header value. It
// does not forward malformed multi-valued or hop-by-hop cache metadata.
func ModelValidator(headers http.Header, name string) string {
	var values []string
	for key, entries := range headers {
		if strings.EqualFold(key, name) {
			values = append(values, entries...)
		}
		if strings.EqualFold(key, "Connection") {
			for _, entry := range entries {
				for _, field := range strings.Split(entry, ",") {
					if strings.EqualFold(strings.TrimSpace(field), name) {
						return ""
					}
				}
			}
		}
	}
	if len(values) != 1 || !validModelValidator(values[0]) {
		return ""
	}
	return values[0]
}

func validModelValidator(value string) bool {
	if len(value) == 0 || len(value) > 1024 {
		return false
	}
	for _, b := range []byte(value) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}
