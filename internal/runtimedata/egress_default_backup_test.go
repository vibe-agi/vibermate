package runtimedata

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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
}
