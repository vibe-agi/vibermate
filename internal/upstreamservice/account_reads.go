// Package upstreamservice describes service-specific operations and safe facts.
// Adapters have no network, secret, account-selection, or retry authority.
package upstreamservice

import (
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

const CodexRateLimits = "codex-account-rate-limits"
const CodexUsageHistory = "codex-account-usage-history"

var ErrUnsupported = errors.New("upstream account operation is unsupported")
var ErrHistoryDenied = errors.New("account history requires explicit route permission")

// Read is a sealed service contract, not an arbitrary HTTP request.
type Read struct {
	id     string
	path   string
	origin originidentity.ProviderOrigin
}

func (read Read) ID() string                            { return read.id }
func (read Read) Path() string                          { return read.path }
func (read Read) Origin() originidentity.ProviderOrigin { return read.origin }
func (read Read) AdapterID() string                     { return "chatgpt-codex" }
func (read Read) AdapterRevision() uint64               { return 1 }
func (read Read) Validate() error {
	resolved, err := ResolveRead(read.origin, read.id)
	if err != nil || resolved.path != read.path {
		return ErrUnsupported
	}
	return nil
}

func (read Read) RequiresHistoryPermission() bool { return read.id == CodexUsageHistory }

// AccountOperations supplies the executable catalog from the same contracts
// used for provider dispatch. Ingress must still select through operationcatalog.
func AccountOperations() []protocolspec.ClientOperationOptions {
	id, _ := protocolspec.NewClientOperationID(CodexRateLimits)
	quota := protocolspec.ClientOperationOptions{
		ID: id, Revision: 1, ClientDialect: protocolspec.DialectOpenAIResponses,
		Methods: []string{http.MethodGet}, PathPattern: "/backend-api/wham/usage",
		PathMatch: protocolspec.ClientOperationPathExact, Kind: protocolspec.ClientOperationAccountRead,
		Transport: protocolspec.ClientOperationTransportHTTP, BodyKind: protocolspec.ClientOperationBodyNone,
		ReplayClass: protocolspec.ClientReplaySafe, PayloadClass: protocolspec.OperationPayloadControl,
		EgressBearing: true,
	}
	history := quota
	history.ID, _ = protocolspec.NewClientOperationID(CodexUsageHistory)
	history.PathPattern = "/backend-api/wham/profiles/me"
	return []protocolspec.ClientOperationOptions{quota, history}
}

// AllowsClientRead recognizes service-owned account reads outside the client's
// model API base path. It does not grant an account lease or history access;
// the frozen route and the reader still enforce those independently. Only an
// exact registered request on the adapter's official client target qualifies.
// Custom Base URLs must retain their configured path scope.
func AllowsClientRead(base originidentity.ProviderOrigin, canonical originidentity.ClientOrigin, request protocolspec.RequestTarget) bool {
	if base.Validate() != nil || canonical.Validate() != nil ||
		base.String() != "https://chatgpt.com/backend-api/codex" || canonical.String() != "https://chatgpt.com" {
		return false
	}
	for _, contract := range AccountOperations() {
		operation, err := protocolspec.NewClientOperationDefinition(contract)
		if err != nil || operation.Kind() != protocolspec.ClientOperationAccountRead {
			continue
		}
		if matched, _, err := operation.Match(request); err == nil && matched {
			return true
		}
	}
	return false
}

func ResolveRead(origin originidentity.ProviderOrigin, id string) (Read, error) {
	if !upstreamendpoint.IsChatGPTCodexOrigin(origin) {
		return Read{}, ErrUnsupported
	}
	for _, contract := range AccountOperations() {
		if contract.ID.String() == id {
			return Read{id: id, path: contract.PathPattern, origin: origin}, nil
		}
	}
	return Read{}, ErrUnsupported
}
