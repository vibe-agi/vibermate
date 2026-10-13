package runtimepersistence

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Catches lost default persistence or resolving a selected proxy as direct.
func TestDefaultEgressPersistsAndResolves(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	defer func() { shutdownTestStore(t, store) }()
	profiles, err := egressprofile.NewManager(store.EgressProfileRepository(), nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := profiles.Publish(ctx, egressprofile.PublishCommand{ID: "profile.default-test", DisplayName: "Synthetic SOCKS", Policy: egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:7890"}, Resolver: egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverSystem, Transport: egressnetwork.ResolverTransportDirect}}})
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := egressprofile.NewDefaultManager(store.EgressDefaultRepository(), profiles)
	if err != nil {
		t.Fatal(err)
	}
	setting, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}})
	if err != nil {
		t.Fatalf("set real default: %v", err)
	}
	if setting.Revision != 2 || setting.Profile == nil || !setting.Profile.Equal(profile) {
		t.Fatalf("setting %+v", setting)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	store = openTestStore(t, path)
	profiles, err = egressprofile.NewManager(store.EgressProfileRepository(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defaults, err = egressprofile.NewDefaultManager(store.EgressDefaultRepository(), profiles)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := defaults.Resolve(ctx, egressprofile.Inherit(), egressprofile.Inherit())
	if err != nil || resolved.Source() != egressprofile.SelectionGlobal || resolved.DefaultRevision() != 2 || !resolved.Profile().Equal(profile) {
		t.Fatalf("reopened resolution %+v err=%v", resolved, err)
	}
}

type defaultCommitFault struct {
	repository            *egressDefaultRepository
	committed, unreadable bool
}

func (f defaultCommitFault) Commit(tx *sql.Tx) error {
	var err error
	if f.committed {
		err = tx.Commit()
	} else {
		err = tx.Rollback()
	}
	if f.unreadable {
		err = errors.Join(err, f.repository.reads.Close())
	}
	return errors.Join(errors.New("synthetic default commit fault"), err)
}

func TestDefaultEgressCommitFaultsReconcileRealState(t *testing.T) {
	for _, mode := range []string{"rollback", "committed", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "runtime.db")
			store := openTestStore(t, path)
			profiles, _ := egressprofile.NewManager(store.EgressProfileRepository(), nil)
			defaults, _ := egressprofile.NewDefaultManager(store.EgressDefaultRepository(), profiles)
			store.egressDefault.committer = defaultCommitFault{repository: store.egressDefault, committed: mode != "rollback", unreadable: mode == "unreadable"}
			setting, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: egressprofile.DirectID, Revision: 1}})
			if mode == "committed" {
				if err != nil || setting.Revision != 2 {
					t.Fatalf("committed proof %+v %v", setting, err)
				}
			} else if err == nil {
				t.Fatal("fault represented as success")
			}
			if mode == "unreadable" {
				if _, err := defaults.Resolve(ctx, egressprofile.Inherit(), egressprofile.Inherit()); err == nil {
					t.Fatal("unreadable commit used stale cache")
				}
			}
			shutdownTestStore(t, store)
			store = openTestStore(t, path)
			defer shutdownTestStore(t, store)
			current, err := store.EgressDefaultRepository().LoadDefault(ctx)
			want := uint64(2)
			if mode == "rollback" {
				want = 1
			}
			if err != nil || current.Revision != want || (current.Profile == nil) != (mode == "rollback") {
				t.Fatalf("durable default %+v %v", current, err)
			}
		})
	}
}

func TestDefaultEgressCASMutationIsolationAndCorruption(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	profiles, _ := egressprofile.NewManager(store.EgressProfileRepository(), nil)
	defaults, _ := egressprofile.NewDefaultManager(store.EgressDefaultRepository(), profiles)
	setting, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: egressprofile.DirectID, Revision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	setting.Profile.DisplayName = "caller mutation"
	current, err := defaults.GetDefault(ctx)
	if err != nil || !current.Profile.Equal(egressprofile.Direct()) {
		t.Fatal("caller changed default")
	}
	if _, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1}); !errors.Is(err, egressprofile.ErrRevisionConflict) {
		t.Fatalf("stale CAS %v", err)
	}
	for _, ref := range []egressprofile.ProfileRef{{ID: "profile.missing", Revision: 1}, {ID: egressprofile.DirectID, Revision: 2}} {
		if _, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 2, Profile: &ref}); err == nil {
			t.Fatal("missing revision accepted")
		}
	}
	for _, query := range []string{`UPDATE runtime_egress_default SET egress_id='profile.missing',egress_revision=1`, `UPDATE runtime_egress_default SET egress_id='profile.direct',egress_revision=2`} {
		if _, err := store.database.Exec(query); err != nil {
			t.Fatal(err)
		}
		if _, err := defaults.Resolve(ctx, egressprofile.Inherit(), egressprofile.Inherit()); err == nil {
			t.Fatal("corrupted selected revision became direct")
		}
	}
	if _, err := store.database.Exec(`DELETE FROM runtime_egress_default`); err != nil {
		t.Fatal(err)
	}
	if _, err := defaults.GetDefault(ctx); err == nil {
		t.Fatal("missing singleton became unset")
	}
	direct, _ := egressprofile.Explicit(egressprofile.Direct())
	if _, err := defaults.Resolve(ctx, direct, egressprofile.Inherit()); err != nil {
		t.Fatal("explicit choice depends on corrupted default")
	}
}

func TestDefaultEgressConcurrentSnapshotsAndCAS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	profiles, _ := egressprofile.NewManager(store.EgressProfileRepository(), nil)
	var published []egressprofile.ProfileRevision
	for i := 0; i < 2; i++ {
		p := egressnetwork.DefaultPolicy()
		p.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: fmt.Sprintf("127.0.0.1:%d", 7890+i)}
		profile, err := profiles.Publish(ctx, egressprofile.PublishCommand{ID: egressprofile.ID(fmt.Sprintf("profile.concurrent-%d", i)), DisplayName: "Synthetic concurrent", Policy: p})
		if err != nil {
			t.Fatal(err)
		}
		published = append(published, profile)
	}
	defaults, _ := egressprofile.NewDefaultManager(store.EgressDefaultRepository(), profiles)
	setting, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: published[0].ID, Revision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	problems := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 40 {
				snapshot, err := defaults.GetDefault(ctx)
				if err != nil {
					problems <- err
					return
				}
				if snapshot.Profile == nil || !(snapshot.Profile.Equal(published[0]) || snapshot.Profile.Equal(published[1])) {
					problems <- errors.New("mixed setting/profile snapshot")
					return
				}
			}
		}()
	}
	for i := 0; i < 16; i++ {
		p := published[(i+1)%2]
		setting, err = defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: setting.Revision, Profile: &egressprofile.ProfileRef{ID: p.ID, Revision: p.Revision}})
		if err != nil {
			t.Error(err)
			break
		}
	}
	wg.Wait()
	close(problems)
	for err := range problems {
		t.Error(err)
	}
	// Two genuinely competing commands at the same revision have one winner.
	var attempts sync.WaitGroup
	results := make(chan error, 2)
	for _, ref := range []*egressprofile.ProfileRef{nil, {ID: egressprofile.DirectID, Revision: 1}} {
		attempts.Add(1)
		go func(ref *egressprofile.ProfileRef) {
			defer attempts.Done()
			_, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: setting.Revision, Profile: ref})
			results <- err
		}(ref)
	}
	attempts.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, egressprofile.ErrRevisionConflict) {
			conflict++
		} else {
			t.Error(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("CAS outcomes success%d conflict%d", success, conflict)
	}
}

//go:embed testdata/pre_global_egress_v2.sql
var preGlobalEgressV2SQL string

func TestDefaultEgressRejectsPrechangePrivateV2WithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	db := openSQLiteForTest(t, path)
	nativeExec(t, db, preGlobalEgressV2SQL)
	nativeExec(t, db, `INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',2,'2026-10-03T00:00:00Z')`)
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InspectSchema(context.Background(), path, time.Second); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("private v2 inspection %v", err)
	}
	store, err := Open(context.Background(), Options{DatabasePath: path, BusyTimeout: DefaultBusyTimeout, CommitReconcileTimeout: DefaultCommitReconcileTimeout})
	if err == nil {
		store.Shutdown(context.Background())
		t.Fatal("private v2 opened")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("private v2 changed")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refusal created %s", suffix)
		}
	}
}

func TestDefaultEgressUpgradeSeedAndCertification(t *testing.T) {
	ctx := context.Background()
	db := openSQLiteForTest(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer db.Close()
	nativeExec(t, db, schemaV1SQL)
	nativeExec(t, db, migration001To002SQL)
	if err := migrationAdditionsEmpty(ctx, db); err != nil {
		t.Fatalf("upgrade defaults invalid %v", err)
	}
	nativeExec(t, db, `UPDATE runtime_egress_default SET revision=2`)
	if err := migrationAdditionsEmpty(ctx, db); err == nil {
		t.Fatal("changed upgrade seed certified")
	}
}
