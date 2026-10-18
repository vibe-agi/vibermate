package runtimedata

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
)

func TestBackupRestoresExactGlobalDefaultRevision(t *testing.T) {
	ctx := context.Background()
	source, _ := backupFixture(t)
	store := openBackupStore(t, filepath.Join(source, "runtime.db"))
	profiles, _ := egressprofile.NewManager(store.EgressProfileRepository(), nil)
	policy := egressnetwork.DefaultPolicy()
	policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:7890"}
	profile, err := profiles.Publish(ctx, egressprofile.PublishCommand{ID: "profile.backup-default", DisplayName: "Synthetic backup", Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	defaults, _ := egressprofile.NewDefaultManager(store.EgressDefaultRepository(), profiles)
	if _, err = defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	attempt, err := egressaudit.New(egressaudit.NewInput{ID: "selection-backup", Purpose: egressaudit.PurposeCredentialRefresh, PayloadClass: egressaudit.PayloadRuntime, Parent: egressaudit.ParentRef{Kind: egressaudit.ParentRuntimeAction, ID: "independent-parent"}, Caller: egressaudit.CallerCore, TargetOrigin: "https://auth.openai.com", Decision: egressaudit.DecisionRef{PolicyID: "runtime.egress-default", PolicyRevision: 2, Authority: egressaudit.AuthorityRuntime, RuleID: "global.default", ProxyID: profile.ID.String(), ProxyRevision: uint64(profile.Revision), SelectionSource: egressprofile.SelectionGlobal, DefaultRevision: 2}, StartedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.EgressAttemptRepository().Append(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	terminal, err := attempt.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, BytesIn: 11, BytesOut: 13, CompletedAt: now.Add(time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.EgressAttemptRepository().Complete(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	local, err := egressaudit.New(egressaudit.NewInput{ID: "transport-backup", Purpose: egressaudit.PurposeUpstreamModelDiscovery, PayloadClass: egressaudit.PayloadRuntime, Parent: egressaudit.ParentRef{Kind: egressaudit.ParentRuntimeAction, ID: "local-parent"}, Caller: egressaudit.CallerCore, TargetOrigin: "http://local.fixture:11434/v1", Decision: egressaudit.DecisionRef{PolicyID: "account.local", PolicyRevision: 1, Authority: egressaudit.AuthorityRuntime, RuleID: "account.egress", ProxyID: "profile.direct", ProxyRevision: 1, SelectionSource: egressprofile.SelectionTransport, AccountID: "local-account", AccountSettingsRevision: 1}, StartedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.EgressAttemptRepository().Append(ctx, local); err != nil {
		t.Fatal(err)
	}
	localTerminal, err := local.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, BytesIn: 17, BytesOut: 19, CompletedAt: now.Add(time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.EgressAttemptRepository().Complete(ctx, localTerminal); err != nil {
		t.Fatal(err)
	}
	if err = store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	freezePublishedBackupFixture(t, source)
	originalAudit := snapshotBackupTable(t, filepath.Join(source, "runtime.db"), "runtime_egress_attempts")
	// The storage owner requires canonical targets; backupFixture's parent is
	// already symlink-resolved, unlike macOS's /var temporary spelling.
	backup, target := filepath.Join(filepath.Dir(source), "default-backup"), filepath.Join(filepath.Dir(source), "default-restore")
	if err = Backup(ctx, source, backup, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, backup, target); err != nil {
		t.Fatal(err)
	}
	restoredControl := openBackupControlReader(t, target)
	setting, err := restoredControl.EgressDefaultRepository().LoadDefault(ctx)
	if err != nil || setting.Revision != 2 || setting.Profile == nil || !setting.Profile.Equal(profile) {
		t.Fatalf("restored default %+v %v", setting, err)
	}
	assertBackupEgressAttempt(t, backupHistoryPath(t, target), terminal)
	assertBackupEgressAttempt(t, backupHistoryPath(t, target), localTerminal)
	restoredAudit := snapshotBackupTable(t, backupHistoryPath(t, target), "runtime_egress_attempts")
	if len(restoredAudit) != 2 || !slices.Equal(originalAudit, restoredAudit) {
		t.Fatal("actual backup/restore changed exact global/transport target, source, bytes or terminal")
	}
}

func assertBackupEgressAttempt(t *testing.T, path string, want egressaudit.Attempt) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const query = `SELECT attempt_id,connection_id,purpose,payload_class,parent_kind,parent_id,parent_exchange_id,caller_kind,caller_id,target_origin,policy_id,policy_revision,policy_authority,rule_id,proxy_id,proxy_revision,account_id,account_settings_revision,selection_source,default_revision,reused_transport,started_at_unix_ms,completed_at_unix_ms,outcome,error_class,bytes_out,bytes_in,byte_counts_known FROM runtime_egress_attempts WHERE attempt_id=?`
	b := func(v bool) int64 {
		if v {
			return 1
		}
		return 0
	}
	d, p := want.Decision(), want.Parent()
	expected := []any{want.ID(), want.ConnectionID(), string(want.Purpose()), string(want.PayloadClass()), string(p.Kind), p.ID, p.ExchangeID, string(want.Caller()), want.CallerID(), want.TargetOrigin(), d.PolicyID, int64(d.PolicyRevision), string(d.Authority), d.RuleID, d.ProxyID, int64(d.ProxyRevision), d.AccountID, int64(d.AccountSettingsRevision), string(d.SelectionSource), int64(d.DefaultRevision), b(want.ReusedTransport()), want.StartedAt().UnixMilli(), want.CompletedAt().UnixMilli(), string(want.Outcome()), want.ErrorClass(), want.BytesOut(), want.BytesIn(), b(want.ByteCountsKnown())}
	actual := make([]any, len(expected))
	ptrs := make([]any, len(expected))
	for i := range actual {
		ptrs[i] = &actual[i]
	}
	if err = db.QueryRow(query, want.ID()).Scan(ptrs...); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expected, actual) {
		t.Fatal(fmt.Sprintf("restored terminal differs: want=%#v got=%#v", expected, actual))
	}
}
