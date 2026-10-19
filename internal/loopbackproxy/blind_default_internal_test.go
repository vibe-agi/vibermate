package loopbackproxy

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/testrecording"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
)

func blindBuiltinResolved(t *testing.T) egressprofile.Resolved {
	t.Helper()
	r, err := egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionBuiltin, 7)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type blindFixedDefault struct{ value egressprofile.Resolved }

func (d blindFixedDefault) Resolve(context.Context, egressprofile.Choice, egressprofile.Choice) (egressprofile.Resolved, error) {
	return d.value, nil
}

func TestBlindAuditCarriesFrozenDefaultSelection(t *testing.T) {
	for _, source := range []egressprofile.SelectionSource{egressprofile.SelectionBuiltin, egressprofile.SelectionGlobal} {
		t.Run(string(source), func(t *testing.T) {
			resolved, err := egressprofile.RestoreResolved(egressprofile.Direct(), source, 7)
			if err != nil {
				t.Fatal(err)
			}
			handler := &Handler{egressAudit: testrecording.Egress(&blindTerminalAuditDouble{}), clock: time.Now, ownerContext: context.Background()}
			attempt, err := handler.beginBlindAudit(context.Background(), "blind-default-attempt", "blind-default-connection", "https://target.example:443", resolved)
			if err != nil {
				t.Fatal(err)
			}
			decision := attempt.Decision()
			policy, revision, rule := egressaudit.BuiltInDirectPolicyID, uint64(1), egressaudit.BuiltInDirectRuleID
			if source == egressprofile.SelectionGlobal {
				policy, revision, rule = "runtime.egress-default", 7, "global.default"
			}
			if decision.SelectionSource != source || decision.DefaultRevision != 7 || decision.PolicyID != policy || decision.PolicyRevision != revision || decision.RuleID != rule || decision.Authority != egressaudit.AuthorityNetwork || decision.ProxyID != resolved.Profile().ID.String() || decision.ProxyRevision != 1 || decision.AccountID != "" || decision.AccountSettingsRevision != 0 {
				t.Fatalf("actual frozen decision lost: %+v", decision)
			}
		})
	}
}
