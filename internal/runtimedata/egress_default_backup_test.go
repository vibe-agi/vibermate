package runtimedata

import (
	"context"
	"path/filepath"
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
	// The storage owner requires canonical targets; backupFixture's parent is
	// already symlink-resolved, unlike macOS's /var temporary spelling.
	backup, target := filepath.Join(filepath.Dir(source), "default-backup"), filepath.Join(filepath.Dir(source), "default-restore")
	if err = Backup(ctx, source, backup, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, backup, target); err != nil {
		t.Fatal(err)
	}
	store = openBackupStore(t, filepath.Join(target, "runtime.db"))
	defer store.Shutdown(ctx)
	setting, err := store.EgressDefaultRepository().LoadDefault(ctx)
	if err != nil || setting.Revision != 2 || setting.Profile == nil || !setting.Profile.Equal(profile) {
		t.Fatalf("restored default %+v %v", setting, err)
	}
	page, err := store.EgressAttemptRepository().List(ctx, egressaudit.PageRequest{Limit: 20})
	if err != nil || len(page.Items) != 2 {
		t.Fatal("actual backup/restore lost typed frozen selection or terminal history", err)
	}
	seen := make(map[string]egressaudit.Attempt)
	for _, item := range page.Items {
		seen[item.Attempt.ID()] = item.Attempt
	}
	if seen[terminal.ID()] != terminal || seen[localTerminal.ID()] != localTerminal {
		t.Fatal("actual backup/restore changed global/transport target, source, bytes or terminal")
	}
}
