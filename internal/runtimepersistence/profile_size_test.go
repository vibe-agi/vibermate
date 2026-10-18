package runtimepersistence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func largeProfilePolicy(lines int) egressnetwork.Policy {
	var rules []string
	for i := 0; i < lines; i++ {
		rules = append(rules, fmt.Sprintf("d%03d.example.com", i))
	}
	p := egressnetwork.DefaultPolicy()
	p.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:1080"}
	p.DirectDomains = egressnetwork.DirectDomains(strings.Join(rules, "\n"))
	return p
}

func TestMigratedPublishedLargeProfileBindsAndStarts(t *testing.T) {
	f := populatedV1MigrationFixture(t)
	if result, err := UpgradeRevision1(t.Context(), f.input(t)); err != nil || !result.Committed {
		t.Fatalf("upgrade: %+v %v", result, err)
	}
	f.assertLegacyPreserved(t)
	state, err := InspectSchema(t.Context(), filepath.Join(f.source, "runtime.db"), time.Second)
	if err != nil || state.Revision != 2 {
		t.Fatalf("legacy stage: %+v %v", state, err)
	}
	if result, err := UpgradeRevision2(t.Context(), revision2InputAfterLegacyUpgrade(t, f.source, f.owner)); err != nil || !result.Committed || result.ToRevision != 3 {
		t.Fatalf("current stage: %+v %v", result, err)
	}
	f.assertLegacyPreserved(t)
	// Extract only from a private copy, retaining committed source WAL bytes.
	source := filepath.Join(f.source, "runtime.db")
	db := openSQLiteForTest(t, source)
	nativeExec(t, db, `PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; UPDATE provider_accounts SET note=note`)
	copyPath := filepath.Join(t.TempDir(), "runtime.db")
	before := map[string][]byte{}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(source + suffix)
		if err != nil || len(data) == 0 {
			t.Fatalf("read retained source %q: %v", suffix, err)
		}
		before[suffix] = data
		if err := os.WriteFile(copyPath+suffix, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for suffix, want := range before {
			got, err := os.ReadFile(source + suffix)
			if err != nil || !bytes.Equal(want, got) {
				t.Errorf("retained source %q changed: %v", suffix, err)
			}
		}
	})
	target := filepath.Join(t.TempDir(), "control.db")
	const layout = "migrated-large-profile"
	proof, err := ExtractControl(t.Context(), ControlExtraction{SourcePath: copyPath, TargetPath: target, LayoutID: layout, ArchiveID: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if proof.Source.Identity != "vibermate-runtime" || proof.Source.Revision != 3 {
		t.Fatalf("wrong extracted source: %+v", proof.Source)
	}
	digest, rows, err := CertifyStagedControlProjection(t.Context(), target, layout, 3)
	if err != nil || digest != proof.ProjectionDigest || rows != proof.Rows {
		t.Fatalf("staged control certification: rows=%d err=%v", rows, err)
	}
	control, err := OpenControl(t.Context(), ControlOptions{DatabasePath: target, LayoutID: layout, BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownTestStore(t, control.store) })
	// Compare original account/configuration/profile columns before the native
	// fixture writes its own environment and capture authority.
	columns := map[string][]string{}
	for _, table := range []string{"provider_accounts", "upstream_endpoints", "environment_revisions", "environment_revision_counters", "egress_profile_heads", "egress_profile_revisions"} {
		columns[table] = f.columns[table]
	}
	if !reflect.DeepEqual(nativeFingerprint(t, db, columns), nativeFingerprint(t, control.store.database, columns)) {
		t.Fatal("extraction changed original account, configuration or profile bytes")
	}
	p, exists, err := control.EgressProfileRepository().LoadRevision(t.Context(), "profile.example", 1)
	if err != nil || !exists {
		t.Fatal(err)
	}
	store, id, revision := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, nil, control.store, p)
	repo := control.NativeToolRepository()
	now := time.Now().UTC()
	nativeAllow(t, store.NativeToolRepository(), id, now)
	receipt, err := repo.Start(t.Context(), id, "migrated-large-profile", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyStartReceipt(t.Context(), receipt, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(t.Context(), id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := store.database.QueryRow(`SELECT profile_json FROM native_tool_binding_egress WHERE operation_id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(p)
	if raw != string(want) {
		t.Fatal("native binding changed old published bytes")
	}
}

// Storage defense only: scalar JSON strings are deliberately not domain-valid
// profiles. This measures SQLite UTF-8 bytes at the two physical ceilings.
func TestProfileStorageDefensiveJSONByteBoundaries(t *testing.T) {
	f := populatedV1MigrationFixture(t)
	if _, err := UpgradeRevision1(t.Context(), f.input(t)); err != nil {
		t.Fatal(err)
	}
	db := openSQLiteForTest(t, filepath.Join(f.source, "runtime.db"))
	defer db.Close()
	for _, tc := range []struct {
		name, query string
		limit       int
		blob        bool
	}{
		{"policy", `UPDATE egress_profile_revisions SET policy_json=? WHERE egress_id='profile.example'`, 65536, true},
		{"account", `UPDATE provider_accounts SET egress_profile_json=? WHERE account_id='native-legacy'`, 69632, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, size := range []int{tc.limit, tc.limit + 1} {
				inside := size - 2
				raw := `"` + strings.Repeat("\u754c", inside/3) + strings.Repeat("a", inside%3) + `"`
				if len(raw) != size || !json.Valid([]byte(raw)) {
					t.Fatal("bad storage boundary fixture")
				}
				var value any = raw
				if tc.blob {
					value = []byte(raw)
				}
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				_, err = tx.Exec(tc.query, value)
				if (err == nil) != (size == tc.limit) {
					t.Errorf("storage size=%d accepted=%v err=%v", size, err == nil, err)
				}
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

type profileSizeClock struct{}

func (profileSizeClock) Now() time.Time { return time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC) }

// An already-publishable 400-line profile must survive the real settings
// manager, Store and reopen; a 960-line list exercises the old policy cap too.
func TestLargeProfileAccountSettingsReopen(t *testing.T) {
	for _, lines := range []int{400, 960} {
		t.Run(fmt.Sprint(lines), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, "runtime.db")
			store := openTestStore(t, path)
			defer func() { shutdownTestStore(t, store) }()
			factory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(dir, "secrets.json"))
			if err != nil {
				t.Fatal(err)
			}
			secrets, err := factory.Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			builtins, err := upstreamendpoint.BuiltInCommands()
			if err != nil {
				t.Fatal(err)
			}
			endpoints, err := upstreamendpoint.NewManager(ctx, store.UpstreamEndpointRepository(), builtins, profileSizeClock{})
			if err != nil {
				t.Fatal(err)
			}
			defer endpoints.Shutdown(ctx)
			accounts, err := provideraccount.NewManager(ctx, store.ProviderAccountRepository(), secrets, endpoints, provideraccount.BuiltInRealms(), profileSizeClock{})
			if err != nil {
				t.Fatal(err)
			}
			defer accounts.Shutdown(ctx)
			material, err := providerauth.NewMaterial("fixture-api-key", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer material.Destroy()
			credential, err := material.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			defer clear(credential)
			value, err := secretstore.NewValue(credential)
			if err != nil {
				t.Fatal(err)
			}
			defer value.Destroy()
			account, err := accounts.Create(ctx, provideraccount.CreateCommand{ID: "account.large", DisplayName: "Large", UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID, Driver: providerauth.AnthropicAPIKeyDriverRef(), Secret: value})
			if err != nil {
				t.Fatal(err)
			}
			profiles, err := egressprofile.NewManager(store.EgressProfileRepository(), profileSizeClock{})
			if err != nil {
				t.Fatal(err)
			}
			name := "Example"
			if lines == 960 {
				name = strings.Repeat("\u754c<&", 40)
			}
			p, err := profiles.Publish(ctx, egressprofile.PublishCommand{ID: "profile.example", DisplayName: name, Policy: largeProfilePolicy(lines)})
			if err != nil {
				t.Fatal("publish accepted domain list", err)
			}
			policyJSON, _ := json.Marshal(p.Policy)
			fullJSON, _ := json.Marshal(p)
			t.Logf("actual bytes domains=%d policy=%d profile=%d", len(p.Policy.DirectDomains), len(policyJSON), len(fullJSON))
			if lines == 400 && (len(p.Policy.DirectDomains) != 6799 || len(policyJSON) != 7322 || len(fullJSON) != 7430) {
				t.Fatal("measured concrete fixture changed")
			}
			if err := accounts.BindEgressProfiles(profiles); err != nil {
				t.Fatal(err)
			}
			selected, err := accounts.SetSettings(ctx, provideraccount.SettingsCommand{ID: account.Account.ID, ExpectedRevision: 1, EgressProfile: p})
			if err != nil {
				t.Fatal("select published account override", err)
			}
			if err := accounts.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if err := endpoints.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			shutdownTestStore(t, store)
			store = openTestStore(t, path)
			loaded, exists, err := store.ProviderAccountRepository().Load(ctx, account.Account.ID)
			if err != nil || !exists || loaded != selected.Account {
				t.Fatalf("reopened settings changed: exists=%v err=%v", exists, err)
			}
			got, exists, err := store.EgressProfileRepository().LoadRevision(ctx, p.ID, p.Revision)
			if err != nil || !exists || !got.Equal(p) {
				t.Fatal("reopened large profile changed", err)
			}
		})
	}
}
