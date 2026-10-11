package providertransport

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

// Owned service operations have no semantic route plan. Their account, wire
// plan and command remain send authority; this target proves only origin TLS
// reachability over the exit already frozen in their managed credential.
func ownedAccountProbeTarget(spec runtimeFetchSpec) (offlinehold.ProbeTarget, error) {
	invalid := errors.New("owned account probe target differs from closed operation")
	if spec.captured != nil || spec.credential == nil || spec.credential.Mode() != providerauth.CredentialManaged ||
		!upstreamendpoint.IsChatGPTCodexOrigin(spec.target.Origin()) {
		return offlinehold.ProbeTarget{}, invalid
	}
	account, ok := spec.credential.Account()
	if !ok || account.Validate() != nil || spec.account != account || spec.targetRef != account.ID || spec.resolved != account.ResolvedEgress {
		return offlinehold.ProbeTarget{}, invalid
	}
	switch spec.purpose {
	case egressaudit.PurposeUpstreamAccountRead:
		if spec.method != http.MethodGet || len(spec.body) != 0 || spec.contentType != "" {
			return offlinehold.ProbeTarget{}, invalid
		}
		matched := false
		for _, operation := range []string{upstreamservice.CodexRateLimits, upstreamservice.CodexUsageHistory, upstreamservice.CodexResetCreditDetails} {
			read, err := upstreamservice.ResolveRead(spec.target.Origin(), operation)
			if err == nil && read.Path() == spec.relativePath {
				matched = true
				break
			}
		}
		if !matched {
			return offlinehold.ProbeTarget{}, invalid
		}
	case egressaudit.PurposeUpstreamAccountAction:
		if spec.method != http.MethodPost || spec.relativePath != resetConsumePath || spec.contentType != "application/json" {
			return offlinehold.ProbeTarget{}, invalid
		}
		var command struct {
			RedeemRequestID string `json:"redeem_request_id"`
			CreditID        string `json:"credit_id"`
		}
		if json.Unmarshal(spec.body, &command) != nil {
			return offlinehold.ProbeTarget{}, invalid
		}
		if _, err := NewOwnedResetRedemption(spec.target.Origin(), command.CreditID, command.RedeemRequestID, spec.credential); err != nil {
			return offlinehold.ProbeTarget{}, invalid
		}
		// Admit exactly the existing producer's command encoding, including
		// its closed field set; duplicate/extra fields and trailing data fail.
		canonical, err := json.Marshal(command)
		if err != nil || !bytes.Equal(canonical, spec.body) {
			return offlinehold.ProbeTarget{}, invalid
		}
	default:
		return offlinehold.ProbeTarget{}, invalid
	}
	target := offlinehold.ProbeTarget{Kind: offlinehold.EgressAuxiliary, Transport: offlinehold.ProbeTransportStrictTLS,
		TargetRef: "https://chatgpt.com", NetworkOrigin: "https://chatgpt.com", HTTPAuthority: "chatgpt.com", TLSServerName: "chatgpt.com", EgressPolicy: spec.egressProfile().Policy}
	return target, target.Validate()
}
