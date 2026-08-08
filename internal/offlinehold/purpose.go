package offlinehold

import (
	"fmt"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

// KindForPurpose projects the fine-grained outbound purpose into Hold's
// deliberately coarser admission and UI-counting taxonomy.
func KindForPurpose(purpose egressaudit.EgressPurpose) (EgressKind, error) {
	switch purpose {
	case egressaudit.PurposeProviderAttempt:
		return EgressProvider, nil
	case egressaudit.PurposeOriginalOrigin:
		return EgressOpaque, nil
	case egressaudit.PurposeRouteOperation, egressaudit.PurposeAgentProbe,
		egressaudit.PurposeAuxiliaryLLM, egressaudit.PurposeLanguageTransform:
		return EgressAuxiliary, nil
	case egressaudit.PurposePluginCatalogSync,
		egressaudit.PurposePluginArtifactFetch:
		return EgressPlugin, nil
	case egressaudit.PurposeUpdate:
		return EgressUpdate, nil
	case egressaudit.PurposeBlindTunnel:
		return EgressBlindTunnel, nil
	default:
		return "", fmt.Errorf("unknown egress purpose %q", purpose)
	}
}
