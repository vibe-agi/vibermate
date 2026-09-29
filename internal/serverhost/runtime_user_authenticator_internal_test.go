package serverhost

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

// The Server Owner configures every Environment, so its own login sessions may
// launch any of them; members have exactly their explicit grants.
func TestOwnerLoginSessionsHoldEveryEnvironment(t *testing.T) {
	t.Parallel()
	none, err := runtimeuser.NewPolicy(false, nil, 7, 9)
	if err != nil {
		t.Fatal(err)
	}
	owner := effectiveRuntimeUserPolicy(none, true)
	if !owner.AllEnvironments() || owner.DailyAgentAPICallWarning != 7 || owner.DailyTokenWarning != 9 {
		t.Fatalf("owner policy = %+v", owner)
	}
	if member := effectiveRuntimeUserPolicy(none, false); member.AllEnvironments() || member.AllowsEnvironment("team") {
		t.Fatalf("member policy was widened: %+v", member)
	}
}
