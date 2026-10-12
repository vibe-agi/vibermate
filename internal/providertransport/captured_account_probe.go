package providertransport

import (
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

// Only a validated captured quota/history GET projects to origin TLS
// reachability. Its frozen plan and managed lease remain send/audit authority.
func capturedAccountProbeTarget(spec runtimeFetchSpec) (offlinehold.ProbeTarget, error) {
	invalid := errors.New("captured account probe target differs from closed operation")
	if spec.purpose != egressaudit.PurposeRouteOperation || spec.captured == nil ||
		spec.method != http.MethodGet || len(spec.body) != 0 || spec.contentType != "" ||
		!upstreamendpoint.IsChatGPTCodexOrigin(spec.target.Origin()) {
		return offlinehold.ProbeTarget{}, invalid
	}
	c := spec.captured
	request, err := NewCapturedAccountRead(c.plan, spec.credential, c.connectionID, c.operationID)
	if err != nil || request.read.Origin() != spec.target.Origin() ||
		request.read.Path() != spec.relativePath || request.targetRef != spec.targetRef {
		return offlinehold.ProbeTarget{}, invalid
	}
	if request.read.ID() != upstreamservice.CodexRateLimits && request.read.ID() != upstreamservice.CodexUsageHistory {
		return offlinehold.ProbeTarget{}, invalid
	}
	account, ok := spec.credential.Account()
	if !ok || account.Validate() != nil || account != spec.account || spec.resolved != account.ResolvedEgress {
		return offlinehold.ProbeTarget{}, invalid
	}
	// Recognized provider base paths belong to the real read, not its TLS probe.
	const origin = "https://chatgpt.com"
	target := offlinehold.ProbeTarget{Kind: offlinehold.EgressAuxiliary,
		Transport: offlinehold.ProbeTransportStrictTLS,
		TargetRef: origin, NetworkOrigin: origin,
		HTTPAuthority: spec.target.HTTPAuthority(), TLSServerName: spec.target.TLSServerName(),
		EgressPolicy: spec.egressProfile().Policy}
	return target, target.Validate()
}
