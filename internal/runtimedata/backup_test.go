package runtimedata

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestBackupRestoreRoundTripExcludesProviderSecrets(t *testing.T) {
	source, expectedBody := backupFixture(t)
	root := filepath.Dir(source)
	backup := filepath.Join(root, "backup")
	restored := filepath.Join(root, "restored")
	createdAt := time.Date(2026, 9, 26, 3, 4, 5, 0, time.UTC)

	if err := Backup(context.Background(), source, backup, createdAt); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBackup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(backup, backupManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), "provider-secret") ||
		strings.Contains(string(manifest), serverSecretDirectory) {
		t.Fatal("backup exported provider credentials")
	}
	manifestInfo, err := os.Stat(filepath.Join(backup, backupManifestName))
	if err != nil || runtime.GOOS != "windows" && manifestInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup manifest permissions = %v, %v", manifestInfo, err)
	}
	if _, err := os.Stat(filepath.Join(backup, serverSecretDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("backup contains the Server SecretStore")
	}
	if _, err := os.Stat(filepath.Join(backup, developmentSecretDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("backup contains the development SecretStore")
	}

	if err := Restore(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(restored, backupManifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("backup manifest became live Runtime data")
	}
	if value, err := os.ReadFile(filepath.Join(restored, "local-ca", "root-key.pem")); err != nil || string(value) != "test-only-root-key" {
		t.Fatalf("restored Proxy CA = %q, %v", value, err)
	}
	store := openBackupStore(t, filepath.Join(restored, "runtime.db"))
	defer shutdownBackupStore(t, store)
	rules, err := store.ConnectionRuleRepository().Load(context.Background())
	if err != nil || rules.Revision != 1 {
		t.Fatalf("restored configuration = %+v, %v", rules, err)
	}
	record, err := store.RawEvidenceRepository().GetEnvelope(
		context.Background(), "backup-envelope",
	)
	if err != nil || string(record.Body) != string(expectedBody) {
		t.Fatalf("restored evidence = %q, %v", record.Body, err)
	}
}

func TestBackupIncludesDurableWAL(t *testing.T) {
	source, _ := backupFixture(t)
	database, err := sql.Open("sqlite", "file:"+filepath.Join(source, "runtime.db")+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(
		`PRAGMA journal_mode = WAL;
		 PRAGMA wal_autocheckpoint = 0;
		 UPDATE connection_rule_sets SET mode = 'deny_unknown' WHERE id = 1`,
	); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(filepath.Dir(source), "wal-backup")
	if err := Backup(
		context.Background(), source, backup,
		time.Date(2026, 9, 26, 3, 4, 5, 0, time.UTC),
	); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(filepath.Dir(source), "wal-restored")
	if err := Restore(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	store := openBackupStore(t, filepath.Join(restored, "runtime.db"))
	defer shutdownBackupStore(t, store)
	rules, err := store.ConnectionRuleRepository().Load(context.Background())
	if err != nil || rules.Mode != connectionpolicy.ModeDenyUnknown {
		t.Fatalf("WAL configuration was lost: %+v, %v", rules, err)
	}
}

func TestBackupValidationRejectsTamperingIncompatibilityAndUnknownTargets(t *testing.T) {
	for _, scenario := range []string{
		"tampered", "extra", "incompatible", "database_incompatible", "busy", "target_exists",
	} {
		t.Run(scenario, func(t *testing.T) {
			source, _ := backupFixture(t)
			root := filepath.Dir(source)
			backup := filepath.Join(root, "backup")
			if scenario == "busy" {
				guard, err := Acquire(source)
				if err != nil {
					t.Fatal(err)
				}
				defer guard.Release()
				if err := Backup(context.Background(), source, backup, time.Now().UTC()); !errors.Is(err, ErrBusy) {
					t.Fatalf("busy backup error = %v", err)
				}
				return
			}
			if err := Backup(
				context.Background(), source, backup,
				time.Date(2026, 9, 26, 3, 4, 5, 0, time.UTC),
			); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "tampered":
				if err := os.WriteFile(
					filepath.Join(backup, "local-ca", "root-certificate.pem"),
					[]byte("changed"), 0o600,
				); err != nil {
					t.Fatal(err)
				}
				if err := ValidateBackup(context.Background(), backup); !errors.Is(err, ErrBackupInvalid) {
					t.Fatalf("tampered backup error = %v", err)
				}
			case "extra":
				if err := os.WriteFile(filepath.Join(backup, "unexpected"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := ValidateBackup(context.Background(), backup); !errors.Is(err, ErrBackupInvalid) {
					t.Fatalf("extra-file backup error = %v", err)
				}
			case "incompatible":
				name := filepath.Join(backup, backupManifestName)
				encoded, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				encoded = []byte(strings.Replace(string(encoded), `"revision":1`, `"revision":2`, 1))
				if err := os.WriteFile(name, encoded, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := ValidateBackup(context.Background(), backup); !errors.Is(err, ErrBackupIncompatible) {
					t.Fatalf("incompatible backup error = %v", err)
				}
			case "database_incompatible":
				database, err := sql.Open("sqlite", "file:"+filepath.Join(backup, "runtime.db")+"?mode=rw")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(
					`PRAGMA ignore_check_constraints = ON;
					 UPDATE runtime_metadata SET schema_revision = 2`,
				); err != nil {
					t.Fatal(err)
				}
				if err := database.Close(); err != nil {
					t.Fatal(err)
				}
				if err := ValidateBackup(context.Background(), backup); !errors.Is(err, ErrBackupIncompatible) {
					t.Fatalf("incompatible database error = %v", err)
				}
			case "target_exists":
				target := filepath.Join(root, "occupied")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(target, "unknown.txt")
				if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := Restore(context.Background(), backup, target); !errors.Is(err, ErrTarget) {
					t.Fatalf("occupied restore error = %v", err)
				}
				if value, err := os.ReadFile(marker); err != nil || string(value) != "keep" {
					t.Fatal("restore changed an unknown target")
				}
			}
			if scenario == "tampered" || scenario == "extra" ||
				scenario == "incompatible" || scenario == "database_incompatible" {
				target := filepath.Join(root, "must-not-exist")
				if err := Restore(context.Background(), backup, target); err == nil {
					t.Fatal("invalid backup restored")
				}
				if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed restore created or replaced its target")
				}
			}
		})
	}
}

func TestBackupRefusesToClaimMissingProxyCA(t *testing.T) {
	source, _ := backupFixture(t)
	if err := os.Remove(filepath.Join(source, "local-ca", "root-key.pem")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(source), "backup")
	if err := Backup(
		context.Background(), source, target,
		time.Date(2026, 9, 26, 3, 4, 5, 0, time.UTC),
	); !errors.Is(err, ErrBackupInvalid) {
		t.Fatalf("missing CA backup error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, backupManifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete backup was given a manifest")
	}
}

func backupFixture(t *testing.T) (string, []byte) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	store := openBackupStore(t, filepath.Join(source, "runtime.db"))
	if _, err := store.ConnectionRuleRepository().Seed(
		context.Background(), connectionpolicy.ShippedSnapshot(1),
		time.Date(2026, 9, 26, 1, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"synthetic-backup"}`)
	digest := sha256.Sum256(body)
	observed := time.Date(2026, 9, 26, 1, 1, 0, 0, time.UTC)
	record := rawevidence.StoredEnvelope{
		EnvelopeID: "backup-envelope", WriterID: "backup-writer", Watermark: 1,
		Layer: rawevidence.LayerClientIngress, ScopeKind: rawevidence.ScopeManagedRun,
		ScopeID: "backup-run", ExchangeID: "backup-exchange",
		ConnectionID: "backup-connection", EnvironmentID: "backup-environment",
		EnvironmentRevision: 1, ClientEndpointID: "backup-endpoint",
		ClientEndpointRevision: 1, ProtocolPlanID: "backup-protocol",
		ProtocolPlanRevision: 1, RouteID: "backup-route", RouteRevision: 1,
		ObservedAt: observed, ExpiresAt: observed.Add(30 * 24 * time.Hour),
		Method: "POST", Scheme: "https", Authority: "api.example.test",
		Path: "/v1/messages", ContentType: "application/json",
		Representation: "http_message", Canonicalization: "go_net_http_v1",
		Body: body, BodyBytes: int64(len(body)), BodySHA256: digest,
		DigestScope: rawevidence.DigestFull, PayloadState: rawevidence.PayloadCaptured,
		PayloadMetadata: []byte(`{"version":1,"headers":[]}`),
	}
	if err := store.RawEvidenceRepository().AppendBatch(
		context.Background(), []rawevidence.StoredEnvelope{record}, observed,
	); err != nil {
		t.Fatal(err)
	}
	shutdownBackupStore(t, store)
	if err := os.MkdirAll(filepath.Join(source, "local-ca"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"root-key.pem": "test-only-root-key", "root-certificate.pem": "test-only-root-cert",
		"root-manifest.json": `{"schema":"test-only"}`,
	} {
		if err := os.WriteFile(filepath.Join(source, "local-ca", name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(source, serverSecretDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(source, serverSecretDirectory, "store.json"),
		[]byte(`{"token":"provider-secret-must-not-leave"}`), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, developmentSecretDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(source, developmentSecretDirectory, "store.json"),
		[]byte(`{"token":"development-provider-secret-must-not-leave"}`), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	return source, body
}

func openBackupStore(t *testing.T, path string) *runtimepersistence.Store {
	t.Helper()
	store, err := runtimepersistence.Open(context.Background(), runtimepersistence.Options{
		DatabasePath: path, BusyTimeout: runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func shutdownBackupStore(t *testing.T, store *runtimepersistence.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
