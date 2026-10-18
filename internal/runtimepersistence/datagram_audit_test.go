package runtimepersistence

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"strings"
	"testing"
	"time"
)

func packetAuditFixture(t *testing.T) egressaudit.Attempt {
	t.Helper()
	a, e := egressaudit.New(egressaudit.NewInput{ID: "packet-attempt", ConnectionID: "packet-connection", Purpose: egressaudit.PurposeDatagram, PayloadClass: egressaudit.PayloadOpaqueDatagram, Parent: egressaudit.ParentRef{Kind: egressaudit.ParentDatagramConnection, ID: "packet-connection"}, Caller: egressaudit.CallerCore, TargetOrigin: "udp://packet.fixture:123", Decision: egressaudit.BuiltInDirectDecision(egressaudit.AuthorityNetwork), StartedAt: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)})
	if e != nil {
		t.Fatal(e)
	}
	return a
}

func TestDatagramRejectsPreviousPrivateV2WithoutMutation(t *testing.T) {
	// Reconstruct exactly the before-Task3B schema. The helper checks its
	// historical SHA256 before creating an isolated file and proving refusal.
	prior := beforeExpandedProfileStorage(schemaSQL)
	for _, member := range []string{"'datagram',\n", "'opaque_datagram',\n", "'datagram_connection',\n"} {
		prior = strings.Replace(prior, member, "", 1)
	}
	prior = strings.Replace(prior, `  , CHECK(purpose <> 'datagram' OR (parent_kind = 'datagram_connection'
    AND parent_exchange_id = '' AND policy_authority = 'network'
    AND caller_kind = 'core' AND caller_id = '' AND account_id = ''
    AND account_settings_revision = 0 AND payload_class = 'opaque_datagram'
    AND target_origin LIKE 'udp://%'))
`, "", 1)
	assertOldEgressSchemaRefusedUnchanged(t, prior, "536d07b1f7429efa63660f53adc3bc09f9ab39288c2d4a6e1ab1a76e9db0e600")
}

// Reconstruct the exact historical candidate, retaining the original digest
// assertion; current schema edits must not rewrite that history.
func beforeExpandedProfileStorage(schema string) string {
	schema = strings.Replace(schema, "  cyber_access_program TEXT NOT NULL DEFAULT 'standard' CHECK(cyber_access_program IN ('standard','daybreak_blue','daybreak_red')),\n", "", 1)
	schema = strings.Replace(schema, "BETWEEN 2 AND 65536 AND json_valid(CAST(policy_json AS TEXT))", "BETWEEN 2 AND 8192 AND json_valid(CAST(policy_json AS TEXT))", 1)
	schema = strings.Replace(schema, "length(CAST(egress_profile_json AS BLOB))<=69632", "length(egress_profile_json)<=4096", 1)
	return strings.Replace(schema, "BETWEEN 2 AND 69632 AND json_valid(profile_json)", "BETWEEN 2 AND 16384 AND json_valid(profile_json)", 1)
}

func TestProfileStorageRejectsPreviousPrivateV2WithoutMutation(t *testing.T) {
	assertOldEgressSchemaRefusedUnchanged(t, beforeExpandedProfileStorage(schemaSQL), "9b34a40b80b1e071971c733a10ab98f20e370f6800b359edcb4915b5e5f49116")
}
func TestDatagramSQLTaxonomyBootstrapAndMigrationStayClosed(t *testing.T) {
	for _, migration := range []bool{false, true} {
		db := nativeSchemaDB(t, migration)
		a := packetAuditFixture(t)
		if _, e := appendEgressAttempt(context.Background(), db, a); e != nil {
			t.Fatal("valid UDP cannot persist", migration, e)
		}
		for _, wrong := range []string{
			"purpose='invented'", "payload_class='opaque_tunnel'", "parent_kind='blind_connection'", "policy_authority='environment'", "parent_exchange_id='invented-exchange'", "caller_kind='plugin',caller_id='plugin.fake'", "account_id='guessed-model',account_settings_revision=1,proxy_revision=1", "target_origin='https://packet.fixture:123'",
		} {
			if _, e := db.ExecContext(t.Context(), "UPDATE runtime_egress_attempts SET "+wrong+" WHERE attempt_id='packet-attempt'"); e == nil {
				t.Fatal("invalid UDP combination persisted", migration, wrong)
			}
		}
		record, e := scanEgressAttempt(db.QueryRowContext(t.Context(), egressAttemptSelect+" WHERE attempt_id='packet-attempt'"))
		if e != nil || record.Attempt.TargetOrigin() != "udp://packet.fixture:123" || record.Attempt.PayloadClass() != egressaudit.PayloadOpaqueDatagram {
			t.Fatal("UDP persisted metadata lost", migration, e)
		}
	}
}
