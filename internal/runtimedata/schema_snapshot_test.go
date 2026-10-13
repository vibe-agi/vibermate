package runtimedata

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// Losing a private store or reacquiring the caller's held lock must fail this
// round trip. File expectations come from the independently seeded source.
func TestSchemaUpgradeSnapshotUsesHeldOwnershipAndPreservesPrivateFiles(t *testing.T) {
	source, body := backupFixture(t)
	account, candidate := seedSnapshotAuthority(t, source)
	canonical, err := environment.CanonicalJSON(candidate)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := environment.Digest(candidate)
	if err != nil {
		t.Fatal(err)
	}
	rowsBefore := snapshotTestRows(t, source)
	ctx := context.Background()
	owner, err := Acquire(source)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	host, err := instanceguard.Acquire(filepath.Join(source, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Release()
	before := snapshotTestFiles(t, source)
	snapshot := filepath.Join(filepath.Dir(source), "private-snapshot")
	if err := CreateSchemaUpgradeSnapshot(ctx, source, snapshot, owner, host, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSchemaUpgradeSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(filepath.Dir(source), "private-restored")
	if err := RestoreSchemaUpgradeSnapshot(ctx, snapshot, restored); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{source, restored} {
		after := snapshotTestFiles(t, directory)
		// SQLite may checkpoint/remove the independently copied WAL. All
		// logical rows are compared below; source DB and WAL remain byte exact.
		if directory == restored {
			delete(after, "runtime.db-wal")
		}
		expected := make(map[string][]byte, len(before))
		for name, value := range before {
			if directory != restored || name != "runtime.db-wal" {
				expected[name] = value
			}
		}
		if len(after) != len(expected) {
			t.Fatalf("persistent inventory changed in %s", filepath.Base(directory))
		}
		for name, value := range expected {
			if !bytes.Equal(value, after[name]) {
				t.Fatalf("persistent file changed: %s", name)
			}
		}
	}
	for _, directory := range []string{snapshot, restored} {
		if !slices.Equal(rowsBefore, snapshotTestRows(t, directory)) {
			t.Fatal("repository rows changed across snapshot/restore")
		}
		if err := filepath.WalkDir(directory, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			want := fs.FileMode(0o600)
			if entry.IsDir() {
				want = 0o700
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != want {
				t.Fatalf("private permissions not enforced: %s", filepath.Base(name))
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	store := openBackupStore(t, filepath.Join(restored, "runtime.db"))
	defer shutdownBackupStore(t, store)
	record, err := store.RawEvidenceRepository().GetEnvelope(ctx, "backup-envelope")
	if err != nil || !bytes.Equal(record.Body, body) {
		t.Fatal("restored evidence changed")
	}
	rules, err := store.ConnectionRuleRepository().Load(ctx)
	if err != nil || rules.Revision != 1 {
		t.Fatal("restored configuration changed")
	}
	loaded, found, err := store.ProviderAccountRepository().Load(ctx, account.ID)
	if err != nil || !found || loaded != account {
		t.Fatal("restored account or secret reference changed")
	}
	aggregate, found, err := store.EnvironmentRepository().LoadRevision(ctx, candidate.ID, 1)
	if err != nil || !found {
		t.Fatal("restored Environment unavailable")
	}
	loadedJSON, err := environment.CanonicalJSON(aggregate)
	if err != nil || !bytes.Equal(canonical, loadedJSON) {
		t.Fatal("restored Environment canonical bytes changed")
	}
	loadedDigest, err := environment.Digest(aggregate)
	if err != nil || loadedDigest != digest {
		t.Fatal("restored Environment digest changed")
	}
	factory, err := hostsecret.NewServerFileFactory(restored)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := factory.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	value, err := secrets.ReadAtRevision(ctx, loaded.SecretRef, 1)
	if err != nil {
		t.Fatal("original secret reference no longer resolves")
	}
	defer value.Destroy()
	secretBytes, err := value.CopyBytes()
	defer clear(secretBytes)
	if err != nil || !bytes.Equal(secretBytes, []byte("snapshot-synthetic-provider-credential")) {
		t.Fatal("restored credential differs")
	}
	if _, err := os.Stat(filepath.Join(restored, "schema-upgrade-manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("snapshot manifest became live data")
	}
	if err := host.Release(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(filepath.Dir(source), "ordinary-backup")
	if err := Backup(ctx, source, public, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{serverSecretDirectory, developmentSecretDirectory} {
		if _, err := os.Stat(filepath.Join(public, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("ordinary backup exported credentials")
		}
	}
	manifest, err := readBackupManifest(public)
	if err != nil || manifest.Recoverable.ProviderCredentials || manifest.Schema != backupManifestSchema {
		t.Fatal("ordinary backup scope changed")
	}
}

func seedSnapshotAuthority(t *testing.T, source string) (provideraccount.Account, environment.Environment) {
	t.Helper()
	ctx := context.Background()
	store := openBackupStore(t, filepath.Join(source, "runtime.db"))
	defer shutdownBackupStore(t, store)
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	commands, err := upstreamendpoint.BuiltInCommands()
	if err != nil {
		t.Fatal(err)
	}
	var command upstreamendpoint.CreateCommand
	for _, candidate := range commands {
		if candidate.ID == upstreamendpoint.AnthropicOfficialID {
			command = candidate
			break
		}
	}
	endpoint := upstreamendpoint.Endpoint{ID: command.ID, DisplayName: command.DisplayName, Origin: command.Origin, RealmID: command.RealmID, BackendProtocols: command.BackendProtocols, Capabilities: command.Capabilities, Drivers: command.Drivers, State: upstreamendpoint.StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := store.UpstreamEndpointRepository().Write(ctx, 0, endpoint); err != nil {
		t.Fatal(err)
	}
	ref, err := secretstore.ParseReference("secret://provider-account/snapshot-account")
	if err != nil {
		t.Fatal(err)
	}
	account := provideraccount.Account{ID: "snapshot-account", DisplayName: "Snapshot account", Origin: command.Origin, AssociationRevision: 1, RealmID: command.RealmID, Driver: providerauth.AnthropicAPIKeyDriverRef(), SecretRef: ref, State: provideraccount.StateActive, Revision: 1, SettingsRevision: 1, CreatedAt: now, UpdatedAt: now}
	account.EgressProfile = egressprofile.Direct()
	profiles, err := egressprofile.NewManager(store.EgressProfileRepository(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var rules []string
	for i := 0; i < 400; i++ {
		rules = append(rules, fmt.Sprintf("d%03d.example.com", i))
	}
	if _, err := profiles.Publish(ctx, egressprofile.PublishCommand{ID: "profile.snapshot-large", DisplayName: "Snapshot large", Policy: egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:1080"}, DirectDomains: egressnetwork.DirectDomains(strings.Join(rules, "\n"))}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProviderAccountRepository().Write(ctx, 0, account); err != nil {
		t.Fatal(err)
	}
	origin, err := originidentity.ParseClientOrigin("https://api.example.test")
	if err != nil {
		t.Fatal(err)
	}
	candidate := environment.Environment{ID: "snapshot-environment", Name: "Snapshot environment", State: environment.StateActive, Revision: 1, ContentRecording: environment.DefaultContentRecordingPolicy(), ClientEndpoints: []environment.ClientEndpoint{{ID: "snapshot-endpoint", Revision: 1, ClientOrigin: origin, ProtocolPlans: []environment.ClientProtocolPlan{{ID: "snapshot-plan", Revision: 1, ClientProtocol: environment.ClientProtocolAnthropicMessages, ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "snapshot-adapter", Revision: 1}, EgressProfile: authoredEgress(t, egressprofile.Direct()), Destination: environment.DestinationPlan{Kind: environment.DestinationKindOriginal}}}}}}
	digest, err := environment.Digest(candidate)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := store.EnvironmentRepository().SaveDraft(ctx, environment.DraftMutation{EnvironmentID: candidate.ID, Candidate: candidate, CandidateDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnvironmentRepository().PublishDraft(ctx, environment.PublishMutation{EnvironmentID: candidate.ID, DraftRevision: draft.Revision, CandidateDigest: digest, Candidate: candidate}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivityRepository().Append(ctx, activity.Record{ID: "snapshot-history", OccurredAt: now, Kind: activity.KindApprovalResolved, SubjectID: "snapshot-exchange", Status: activity.StatusFailed, ReasonCode: "provider_transport_failed"}); err != nil {
		t.Fatal(err)
	}
	// Replace the earlier opaque synthetic fixture with the actual unchanged
	// Server SecretStore format so the restored account can resolve its ref.
	if err := os.Remove(filepath.Join(source, serverSecretDirectory, "store.json")); err != nil {
		t.Fatal(err)
	}
	factory, err := hostsecret.NewServerFileFactory(source)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := factory.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	value, err := secretstore.NewValue([]byte("snapshot-synthetic-provider-credential"))
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	if _, err := secrets.Replace(ctx, secretstore.ReplaceCommand{Reference: ref, Value: value}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "unknown-persistent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "unknown-persistent", "future.dat"), []byte("preserve unknown persistent files"), 0o700); err != nil {
		t.Fatal(err)
	}
	return account, candidate
}

// Compare every logical row independently of SQLite page layout/checkpointing.
func snapshotTestRows(t *testing.T, directory string) []string {
	t.Helper()
	u := url.URL{Scheme: "file", Path: filepath.Join(directory, "runtime.db"), RawQuery: "mode=ro"}
	database, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rows, err := database.Query("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	var result []string
	for _, table := range tables {
		rows, err := database.Query(fmt.Sprintf("SELECT * FROM %q", table))
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			result = append(result, table+":"+string(encoded))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(result)
	return result
}

func TestSchemaUpgradeSnapshotHoldsDestinationHostOwnership(t *testing.T) {
	source, _ := backupFixture(t)
	owner, err := Acquire(source)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	target := filepath.Join(filepath.Dir(source), "snapshot")
	runner := newSchemaSnapshotRunner()
	copyFile := runner.copyFile
	runner.copyFile = func(ctx context.Context, from, to string, info fs.FileInfo) error {
		host, err := instanceguard.Acquire(filepath.Join(target, "server.lock"))
		if host != nil {
			_ = host.Release()
		}
		if !errors.Is(err, instanceguard.ErrAlreadyOwned) {
			return errors.New("destination host ownership missing")
		}
		return copyFile(ctx, from, to, info)
	}
	if err := runner.create(context.Background(), source, target, owner, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaUpgradeSnapshotRejectsUnrepresentablePersistentPath(t *testing.T) {
	source, _ := backupFixture(t)
	if err := os.WriteFile(filepath.Join(source, "unsafe:name"), []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner, err := Acquire(source)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	target := filepath.Join(filepath.Dir(source), "snapshot")
	if err := CreateSchemaUpgradeSnapshot(context.Background(), source, target, owner, nil, time.Now().UTC()); err == nil {
		t.Fatal("created an unverifiable snapshot")
	}
	if _, err := os.Stat(filepath.Join(target, schemaSnapshotManifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed snapshot has completion manifest")
	}
}

func TestSchemaUpgradeSnapshotCommittedUncheckpointedWAL(t *testing.T) {
	source, _ := backupFixture(t)
	u := url.URL{Scheme: "file", Path: filepath.Join(source, "runtime.db"), RawQuery: "mode=rw"}
	database, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	if _, err := database.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; UPDATE connection_rule_sets SET mode='deny_unknown' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	wal, err := os.Stat(filepath.Join(source, "runtime.db-wal"))
	if err != nil || wal.Size() <= 32 {
		t.Fatal("fixture has no committed WAL")
	}
	owner, err := Acquire(source)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	before := snapshotTestFiles(t, source)
	rowsBefore := snapshotTestRows(t, source)
	target := filepath.Join(filepath.Dir(source), "wal-snapshot")
	if err := CreateSchemaUpgradeSnapshot(context.Background(), source, target, owner, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	after := snapshotTestFiles(t, source)
	for name, value := range before {
		if !bytes.Equal(value, after[name]) {
			t.Fatalf("source file mutated: %s", name)
		}
	}
	if len(after) != len(before) {
		t.Fatal("source inventory mutated")
	}
	if err := ValidateSchemaUpgradeSnapshot(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(filepath.Dir(source), "wal-restored")
	if err := RestoreSchemaUpgradeSnapshot(context.Background(), target, restored); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rowsBefore, snapshotTestRows(t, restored)) {
		t.Fatal("committed WAL rows lost")
	}
	store := openBackupStore(t, filepath.Join(restored, "runtime.db"))
	defer shutdownBackupStore(t, store)
	rules, err := store.ConnectionRuleRepository().Load(context.Background())
	if err != nil || string(rules.Mode) != "deny_unknown" {
		t.Fatal("committed WAL configuration lost")
	}
}

func TestSchemaUpgradeSnapshotOwnershipAndInvalidTargets(t *testing.T) {
	for _, scenario := range []string{"wrong_source_guard", "released_source_guard", "wrong_host_guard", "released_host_guard", "missing_host_guard_live_server", "correct_held_host_guard", "existing", "nested", "ancestor", "relative", "unclean", "alias_parent", "alias_source", "canceled_before_creation", "non_utc", "nil_context"} {
		t.Run(scenario, func(t *testing.T) {
			source, _ := backupFixture(t)
			actualSource := source
			root := filepath.Dir(source)
			target := filepath.Join(root, "snapshot")
			owner, err := Acquire(source)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Release()
			host, err := instanceguard.Acquire(filepath.Join(source, "server.lock"))
			if err != nil {
				t.Fatal(err)
			}
			defer host.Release()
			ctx := context.Background()
			created := time.Now().UTC()
			before := snapshotTestFiles(t, source)
			expect := ErrTarget
			switch scenario {
			case "wrong_source_guard":
				owner = host
			case "released_source_guard":
				if err := owner.Release(); err != nil {
					t.Fatal(err)
				}
			case "wrong_host_guard":
				host = owner
			case "released_host_guard":
				if err := host.Release(); err != nil {
					t.Fatal(err)
				}
			case "missing_host_guard_live_server":
				host = nil
				expect = ErrBusy
			case "correct_held_host_guard":
				expect = nil
			case "existing":
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "keep"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "nested":
				target = filepath.Join(source, "nested")
			case "ancestor":
				target = root
			case "relative":
				target = "snapshot"
			case "unclean":
				target = root + "/./snapshot"
			case "alias_parent":
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(root, alias); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(alias, "snapshot")
			case "alias_source":
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(source, alias); err != nil {
					t.Fatal(err)
				}
				source = alias
			case "canceled_before_creation":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
				expect = context.Canceled
			case "non_utc":
				created = time.Now().In(time.FixedZone("other", 3600))
			case "nil_context":
				ctx = nil
			}
			err = CreateSchemaUpgradeSnapshot(ctx, source, target, owner, host, created)
			if expect == nil {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, expect) {
					t.Fatalf("error = %v; want %v", err, expect)
				}
				if scenario == "existing" {
					value, err := os.ReadFile(filepath.Join(target, "keep"))
					if err != nil || string(value) != "keep" {
						t.Fatal("existing target changed")
					}
				} else if scenario != "ancestor" {
					if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("preflight failure created target")
					}
				}
			}
			after := snapshotTestFiles(t, actualSource)
			for name, value := range before {
				if !bytes.Equal(value, after[name]) {
					t.Fatalf("source changed: %s", name)
				}
			}
		})
	}
}

func TestSchemaUpgradeSnapshotValidationRejectsTampering(t *testing.T) {
	for _, scenario := range []string{"private_tampered", "private_missing", "extra_file", "symlink", "manifest_symlink", "traversal", "backslash", "duplicate_entry", "duplicate_json_member", "unknown_json_member", "wrong_format", "revision2", "revision99", "foreign_schema", "malformed_known_schema", "manifest_revision", "busy_runtime", "busy_host", "ordinary_backup"} {
		t.Run(scenario, func(t *testing.T) {
			source, _ := backupFixture(t)
			target := filepath.Join(filepath.Dir(source), "snapshot")
			ctx := context.Background()
			if scenario == "ordinary_backup" {
				if err := Backup(ctx, source, target, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(target, backupManifestName), filepath.Join(target, schemaSnapshotManifestName)); err != nil {
					t.Fatal(err)
				}
			} else {
				owner, err := Acquire(source)
				if err != nil {
					t.Fatal(err)
				}
				err = CreateSchemaUpgradeSnapshot(ctx, source, target, owner, nil, time.Now().UTC())
				_ = owner.Release()
				if err != nil {
					t.Fatal(err)
				}
			}
			manifestName := filepath.Join(target, schemaSnapshotManifestName)
			secretName := filepath.Join(target, serverSecretDirectory, "store.json")
			var hold *instanceguard.Guard
			expect := ErrBackupInvalid
			switch scenario {
			case "private_tampered":
				if err := os.WriteFile(secretName, []byte("tampered"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "private_missing":
				if err := os.Remove(secretName); err != nil {
					t.Fatal(err)
				}
			case "extra_file":
				if err := os.WriteFile(filepath.Join(target, "unknown-extra"), []byte("extra"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(secretName, filepath.Join(target, "linked")); err != nil {
					t.Fatal(err)
				}
			case "manifest_symlink":
				if err := os.Rename(manifestName, manifestName+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(manifestName+".real", manifestName); err != nil {
					t.Fatal(err)
				}
			case "busy_runtime", "busy_host":
				name := lockName
				if scenario == "busy_host" {
					name = "server.lock"
				}
				var err error
				hold, err = instanceguard.Acquire(filepath.Join(target, name))
				if err != nil {
					t.Fatal(err)
				}
				defer hold.Release()
				expect = ErrBusy
			case "revision2", "revision99", "foreign_schema", "malformed_known_schema":
				u := url.URL{Scheme: "file", Path: filepath.Join(target, "runtime.db"), RawQuery: "mode=rw"}
				db, err := sql.Open("sqlite", u.String())
				if err != nil {
					t.Fatal(err)
				}
				statement := "UPDATE runtime_metadata SET schema_revision=99"
				if scenario == "revision99" {
					statement = "UPDATE runtime_metadata SET schema_revision=99"
				}
				if scenario == "foreign_schema" {
					statement = "UPDATE runtime_metadata SET schema_identity='foreign-runtime'"
				}
				if scenario == "malformed_known_schema" {
					statement = "DROP TABLE runtime_usage_policy"
				}
				if _, err := db.Exec("PRAGMA ignore_check_constraints=ON;" + statement); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				manifest, err := readSchemaSnapshotManifest(target)
				if err != nil {
					t.Fatal(err)
				}
				manifest.Files, err = schemaSnapshotFiles(ctx, target, true)
				if err != nil {
					t.Fatal(err)
				}
				writeSnapshotTestManifest(t, target, manifest)
				expect = ErrBackupIncompatible
			case "traversal", "backslash", "duplicate_entry", "wrong_format", "manifest_revision":
				manifest, err := readSchemaSnapshotManifest(target)
				if err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "traversal":
					manifest.Files[0].Path = "../escape"
				case "backslash":
					manifest.Files[0].Path = "..\\escape"
				case "duplicate_entry":
					manifest.Files = append([]backupFile{manifest.Files[0]}, manifest.Files...)
				case "wrong_format":
					manifest.Schema = backupManifestSchema
				case "manifest_revision":
					manifest.Database.Revision = 99
					expect = ErrBackupIncompatible
				}
				writeSnapshotTestManifest(t, target, manifest)
			case "duplicate_json_member", "unknown_json_member":
				encoded, err := os.ReadFile(manifestName)
				if err != nil {
					t.Fatal(err)
				}
				prefix := `{"schema":"runtime-schema-upgrade/v1",`
				if scenario == "unknown_json_member" {
					prefix = `{"unknown":true,`
				}
				encoded = append([]byte(prefix), encoded[1:]...)
				if err := os.WriteFile(manifestName, encoded, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := ValidateSchemaUpgradeSnapshot(ctx, target); !errors.Is(err, expect) {
				t.Fatalf("validation error = %v; want %v", err, expect)
			}
			restored := filepath.Join(filepath.Dir(source), "must-not-exist")
			if err := RestoreSchemaUpgradeSnapshot(ctx, target, restored); !errors.Is(err, expect) {
				t.Fatalf("restore error = %v; want %v", err, expect)
			}
			if _, err := os.Lstat(restored); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid/busy snapshot created restore target")
			}
			if hold != nil {
				_ = hold.Release()
			}
			assertSnapshotLocksReleased(t, target)
		})
	}
}

func writeSnapshotTestManifest(t *testing.T, directory string, manifest schemaSnapshotManifest) {
	t.Helper()
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, schemaSnapshotManifestName), append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertSnapshotLocksReleased(t *testing.T, directory string) {
	t.Helper()
	for _, name := range []string{lockName, "server.lock"} {
		guard, err := instanceguard.Acquire(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("lock leaked: %s", name)
		}
		if err := guard.Release(); err != nil {
			t.Fatal(err)
		}
	}
}

type failingSnapshotManifestFile struct {
	snapshotManifestFile
	failure string
}

func (file failingSnapshotManifestFile) Write(encoded []byte) (int, error) {
	if file.failure == "write" {
		_, _ = file.snapshotManifestFile.Write(encoded[:len(encoded)/2])
		return len(encoded) / 2, io.ErrShortWrite
	}
	return file.snapshotManifestFile.Write(encoded)
}
func (file failingSnapshotManifestFile) Sync() error {
	if file.failure == "manifest_sync" {
		return errors.New("synthetic sync failure")
	}
	return file.snapshotManifestFile.Sync()
}

func TestSchemaUpgradeSnapshotFailureHasNoCompletionManifest(t *testing.T) {
	for _, scenario := range []string{"copy", "source_mutation_during_copy", "cancel_during_copy", "manifest_create", "write", "manifest_sync", "file_sync", "directory_sync", "final_directory_sync", "parent_sync", "cancel_final_sync", "destination_mutation_final_sync", "release_source_guard", "release_host_guard", "source_foreign", "source_revision2", "source_revision99", "source_symlink"} {
		t.Run(scenario, func(t *testing.T) {
			source, _ := backupFixture(t)
			owner, err := Acquire(source)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Release()
			host, err := instanceguard.Acquire(filepath.Join(source, "server.lock"))
			if err != nil {
				t.Fatal(err)
			}
			defer host.Release()
			target := filepath.Join(filepath.Dir(source), "snapshot")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := newSchemaSnapshotRunner()
			before := snapshotTestFiles(t, source)
			copyFile := runner.copyFile
			syncPath := runner.syncPath
			createManifest := runner.createManifest
			injected := false
			if scenario == "copy" || scenario == "source_mutation_during_copy" || scenario == "cancel_during_copy" || scenario == "release_source_guard" || scenario == "release_host_guard" {
				runner.copyFile = func(ctx context.Context, from, to string, info fs.FileInfo) error {
					if !injected {
						injected = true
						switch scenario {
						case "copy":
							return errors.New("synthetic copy failure")
						case "source_mutation_during_copy":
							if err := os.WriteFile(filepath.Join(source, serverSecretDirectory, "store.json"), []byte("synthetic mutation"), 0o600); err != nil {
								return err
							}
						case "cancel_during_copy":
							cancel()
						case "release_source_guard":
							_ = owner.Release()
						case "release_host_guard":
							_ = host.Release()
						}
					}
					return copyFile(ctx, from, to, info)
				}
			}
			if scenario == "manifest_create" || scenario == "write" || scenario == "manifest_sync" {
				runner.createManifest = func(name string) (snapshotManifestFile, error) {
					if scenario == "manifest_create" {
						return nil, errors.New("synthetic create failure")
					}
					file, err := createManifest(name)
					if err != nil {
						return nil, err
					}
					return failingSnapshotManifestFile{file, scenario}, nil
				}
			}
			if strings.Contains(scenario, "sync") && scenario != "manifest_sync" {
				runner.syncPath = func(name string) error {
					info, err := os.Stat(name)
					if err != nil {
						return err
					}
					_, manifestErr := os.Stat(filepath.Join(target, schemaSnapshotManifestName))
					fail := scenario == "file_sync" && !info.IsDir() || scenario == "directory_sync" && info.IsDir() || scenario == "final_directory_sync" && name == target && manifestErr == nil || scenario == "parent_sync" && name == filepath.Dir(target)
					if fail {
						return errors.New("synthetic sync failure")
					}
					if scenario == "cancel_final_sync" && name == filepath.Dir(target) {
						cancel()
					}
					if scenario == "destination_mutation_final_sync" && name == filepath.Dir(target) {
						if err := os.WriteFile(filepath.Join(target, serverSecretDirectory, "store.json"), []byte("synthetic destination mutation"), 0o600); err != nil {
							return err
						}
					}
					return syncPath(name)
				}
			}
			if strings.HasPrefix(scenario, "source_") && scenario != "source_mutation_during_copy" && scenario != "source_symlink" {
				u := url.URL{Scheme: "file", Path: filepath.Join(source, "runtime.db"), RawQuery: "mode=rw"}
				db, err := sql.Open("sqlite", u.String())
				if err != nil {
					t.Fatal(err)
				}
				statement := "UPDATE runtime_metadata SET schema_identity='foreign-runtime'"
				if scenario == "source_revision2" {
					statement = "UPDATE runtime_metadata SET schema_revision=99"
				}
				if scenario == "source_revision99" {
					statement = "UPDATE runtime_metadata SET schema_revision=99"
				}
				if _, err := db.Exec("PRAGMA ignore_check_constraints=ON;" + statement); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				before = snapshotTestFiles(t, source)
			}
			if scenario == "source_symlink" {
				if err := os.Symlink(filepath.Join(source, "runtime.db"), filepath.Join(source, "link")); err != nil {
					t.Fatal(err)
				}
				before = snapshotTestFiles(t, source)
			}
			if err := runner.create(ctx, source, target, owner, host, time.Now().UTC()); err == nil {
				t.Fatal("injected failure succeeded")
			}
			if _, err := os.Lstat(filepath.Join(target, schemaSnapshotManifestName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed snapshot has completion manifest")
			}
			if scenario != "source_mutation_during_copy" {
				after := snapshotTestFiles(t, source)
				for name, value := range before {
					if !bytes.Equal(value, after[name]) {
						t.Fatalf("failure modified source: %s", name)
					}
				}
				if len(before) != len(after) {
					t.Fatal("failure modified source inventory")
				}
			}
			if info, err := os.Stat(target); err == nil && info.IsDir() {
				assertSnapshotLocksReleased(t, target)
			}
			_ = owner.Release()
			_ = host.Release()
			assertSnapshotLocksReleased(t, source)
		})
	}
}

func TestSchemaUpgradeSnapshotWaitsForFinalDurabilityBarrier(t *testing.T) {
	source, _ := backupFixture(t)
	owner, err := Acquire(source)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	target := filepath.Join(filepath.Dir(source), "snapshot")
	runner := newSchemaSnapshotRunner()
	syncPath := runner.syncPath
	reached := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	runner.syncPath = func(name string) error {
		if name == filepath.Dir(target) {
			close(reached)
			<-resume
		}
		return syncPath(name)
	}
	go func() { done <- runner.create(context.Background(), source, target, owner, nil, time.Now().UTC()) }()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		close(resume)
		t.Fatal("final sync barrier not reached")
	}
	select {
	case err := <-done:
		close(resume)
		t.Fatalf("returned before parent sync: %v", err)
	default:
	}
	if err := ValidateSchemaUpgradeSnapshot(context.Background(), target); !errors.Is(err, ErrBusy) {
		close(resume)
		t.Fatalf("snapshot exposed without ownership during publication: %v", err)
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSchemaUpgradeSnapshotRestoreFailuresPreserveAuthority(t *testing.T) {
	for _, scenario := range []string{"copy_failure", "sync_failure", "cancel_during_copy", "destination_mutation_final_sync", "snapshot_mutation_final_sync"} {
		t.Run(scenario, func(t *testing.T) {
			source, _ := backupFixture(t)
			snapshot := filepath.Join(filepath.Dir(source), "snapshot")
			owner, err := Acquire(source)
			if err != nil {
				t.Fatal(err)
			}
			err = CreateSchemaUpgradeSnapshot(context.Background(), source, snapshot, owner, nil, time.Now().UTC())
			_ = owner.Release()
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotTestFiles(t, snapshot)
			target := filepath.Join(filepath.Dir(source), "restored")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := newSchemaSnapshotRunner()
			copyFile := runner.copyFile
			syncPath := runner.syncPath
			if scenario == "copy_failure" || scenario == "cancel_during_copy" {
				runner.copyFile = func(ctx context.Context, from, to string, info fs.FileInfo) error {
					if scenario == "copy_failure" {
						return errors.New("synthetic restore copy failure")
					}
					cancel()
					return copyFile(ctx, from, to, info)
				}
			} else {
				runner.syncPath = func(name string) error {
					if scenario == "sync_failure" {
						return errors.New("synthetic restore sync failure")
					}
					_, targetErr := os.Stat(target)
					if name == filepath.Dir(target) && targetErr == nil {
						directory := target
						if scenario == "snapshot_mutation_final_sync" {
							directory = snapshot
						}
						if err := os.WriteFile(filepath.Join(directory, serverSecretDirectory, "store.json"), []byte("synthetic mutation"), 0o600); err != nil {
							return err
						}
					}
					return syncPath(name)
				}
			}
			if err := runner.restore(ctx, snapshot, target); err == nil {
				t.Fatal("restore failure returned success")
			}
			if _, err := os.Lstat(filepath.Join(target, schemaSnapshotManifestName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("snapshot manifest became live data")
			}
			if scenario != "snapshot_mutation_final_sync" {
				after := snapshotTestFiles(t, snapshot)
				if len(before) != len(after) {
					t.Fatal("restore modified snapshot inventory")
				}
				for name, value := range before {
					if !bytes.Equal(value, after[name]) {
						t.Fatalf("restore modified snapshot: %s", name)
					}
				}
			}
			assertSnapshotLocksReleased(t, snapshot)
			if _, err := os.Stat(target); err == nil {
				assertSnapshotLocksReleased(t, target)
			}
		})
	}
}

func TestSchemaUpgradeSnapshotCompositeCleanupFailureRequiresDurabilityRecertification(t *testing.T) {
	source, _ := backupFixture(t)
	owner, err := Acquire(source)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	before := snapshotTestFiles(t, source)
	target := filepath.Join(filepath.Dir(source), "snapshot")
	runner := newSchemaSnapshotRunner()
	syncPath := runner.syncPath
	cleanupAttempted := false
	runner.removeManifest = func(name string) error { cleanupAttempted = true; return fs.ErrPermission }
	runner.syncPath = func(name string) error {
		if name == filepath.Dir(target) || cleanupAttempted {
			return errors.New("synthetic final/cleanup sync failure")
		}
		return syncPath(name)
	}
	if err := runner.create(context.Background(), source, target, owner, nil, time.Now().UTC()); !errors.Is(err, ErrCopy) {
		t.Fatalf("uncertain snapshot creation = %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, schemaSnapshotManifestName)); err != nil {
		t.Fatal("composite fault did not retain uncertain receipt")
	}
	blocked := newSchemaSnapshotRunner()
	blocked.syncPath = func(string) error { return errors.New("synthetic recertification durability failure") }
	if err := blocked.validate(context.Background(), target); !errors.Is(err, ErrCopy) {
		t.Fatalf("validated without recertifying durability: %v", err)
	}
	restored := filepath.Join(filepath.Dir(source), "must-not-exist")
	if err := blocked.restore(context.Background(), target, restored); !errors.Is(err, ErrCopy) {
		t.Fatalf("restored uncertain receipt without recertification: %v", err)
	}
	if _, err := os.Lstat(restored); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed recertification created restore target")
	}
	// A later independent successful validation certifies the complete bytes,
	// known schema, every file/directory and the parent entry afresh.
	if err := ValidateSchemaUpgradeSnapshot(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSchemaUpgradeSnapshot(context.Background(), target, restored); err != nil {
		t.Fatal(err)
	}
	after := snapshotTestFiles(t, source)
	if len(before) != len(after) {
		t.Fatal("composite failure changed source inventory")
	}
	for name, value := range before {
		if !bytes.Equal(value, after[name]) {
			t.Fatalf("composite failure changed source: %s", name)
		}
	}
	assertSnapshotLocksReleased(t, target)
	assertSnapshotLocksReleased(t, restored)
}

func snapshotTestFiles(t *testing.T, directory string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.WalkDir(directory, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(directory, name)
		if err != nil || entry.IsDir() || rel == lockName || rel == "server.lock" || rel == "runtime.db-shm" {
			return err
		}
		value, err := os.ReadFile(name)
		if err == nil {
			files[filepath.ToSlash(rel)] = value
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
