package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
)

func TestEgressSelectionPersistsExactFrozenDefault(t *testing.T) {
	store := openTestStore(t, t.TempDir()+"/runtime.db")
	defer shutdownTestStore(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	attempt, err := egressaudit.New(egressaudit.NewInput{ID: "global-attempt", Purpose: egressaudit.PurposeCredentialRefresh, PayloadClass: egressaudit.PayloadRuntime, Parent: egressaudit.ParentRef{Kind: egressaudit.ParentRuntimeAction, ID: "login-action"}, Caller: egressaudit.CallerCore, TargetOrigin: "https://auth.openai.com", Decision: egressaudit.DecisionRef{PolicyID: "runtime.egress-default", PolicyRevision: 7, Authority: egressaudit.AuthorityRuntime, RuleID: "global.default", ProxyID: "profile.synthetic", ProxyRevision: 3, SelectionSource: egressprofile.SelectionGlobal, DefaultRevision: 7}, StartedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.EgressAttemptRepository().Append(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	stored, err := scanEgressAttempt(store.database.QueryRow(egressAttemptSelect+` WHERE attempt_id=?`, attempt.ID()))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Attempt.Decision() != attempt.Decision() {
		t.Fatal("stored audit lost effective selection/default revision")
	}
	terminal, err := attempt.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, BytesIn: 13, BytesOut: 17, CompletedAt: now.Add(time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.EgressAttemptRepository().Complete(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	stored, err = scanEgressAttempt(store.database.QueryRow(egressAttemptSelect+` WHERE attempt_id=?`, attempt.ID()))
	if err != nil || stored.Attempt != terminal {
		t.Fatal("completion changed frozen selection", err)
	}
}

func TestTransportSelectionPersistsAndRejectsBorrowedDurableTarget(t *testing.T) {
	store := openTestStore(t, t.TempDir()+"/runtime.db")
	defer shutdownTestStore(t, store)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	a, err := egressaudit.New(egressaudit.NewInput{ID: "local-selection", Purpose: egressaudit.PurposeUpstreamModelDiscovery, PayloadClass: egressaudit.PayloadRuntime, Parent: egressaudit.ParentRef{Kind: egressaudit.ParentRuntimeAction, ID: "independent-action"}, Caller: egressaudit.CallerCore, TargetOrigin: "http://127.0.0.1:12345/v1", Decision: egressaudit.DecisionRef{PolicyID: "account.local", PolicyRevision: 1, Authority: egressaudit.AuthorityRuntime, RuleID: "account.egress", ProxyID: "profile.direct", ProxyRevision: 1, SelectionSource: egressprofile.SelectionTransport, AccountID: "local-account", AccountSettingsRevision: 1}, StartedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.EgressAttemptRepository().Append(ctx, a); err != nil {
		t.Fatal("SQL rejected valid constrained transport fact", err)
	}
	terminal, err := a.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, BytesIn: 7, BytesOut: 11, CompletedAt: now.Add(time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.EgressAttemptRepository().Complete(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	stored, err := scanEgressAttempt(store.database.QueryRow(egressAttemptSelect+` WHERE attempt_id=?`, a.ID()))
	if err != nil || stored.Attempt != terminal {
		t.Fatal("stored transport fact changed", err)
	}
	for _, change := range []string{"default_revision=1", "proxy_id='named.direct'", "proxy_revision=2"} {
		if _, err := store.database.Exec(`UPDATE runtime_egress_attempts SET ` + change + ` WHERE attempt_id='local-selection'`); err == nil {
			t.Fatal("SQL accepted malformed transport fact", change)
		}
	}
	for _, origin := range []string{"https://provider.example", "https://127.0.0.1:12345", "http://203.0.113.2:12345"} {
		if _, err = store.database.Exec(`UPDATE runtime_egress_attempts SET target_origin=? WHERE attempt_id=?`, origin, a.ID()); err != nil {
			t.Fatal(err)
		}
		if _, err = scanEgressAttempt(store.database.QueryRow(egressAttemptSelect+` WHERE attempt_id=?`, a.ID())); err == nil {
			t.Fatal("durable restore borrowed local exemption for incompatible target")
		}
	}
}

const priorD3EgressSelectionColumns = `  selection_source TEXT NOT NULL DEFAULT '' CHECK(selection_source IN('', 'account', 'flow', 'global', 'builtin')),
  default_revision INTEGER NOT NULL DEFAULT 0 CHECK(default_revision >= 0)
  CHECK((selection_source IN('', 'account', 'flow') AND default_revision = 0) OR
        (selection_source IN('global', 'builtin') AND default_revision > 0))
  CHECK(selection_source = '' OR proxy_revision > 0)
  CHECK(selection_source <> 'builtin' OR (proxy_id = 'profile.direct' AND proxy_revision = 1)),
`

const egressSelectionColumns = `  selection_source TEXT NOT NULL DEFAULT '' CHECK(selection_source IN('', 'account', 'flow', 'global', 'builtin', 'transport')),
  default_revision INTEGER NOT NULL DEFAULT 0 CHECK(default_revision >= 0)
  CHECK((selection_source IN('', 'account', 'flow', 'transport') AND default_revision = 0) OR
        (selection_source IN('global', 'builtin') AND default_revision > 0))
  CHECK(selection_source = '' OR proxy_revision > 0)
  CHECK(selection_source NOT IN('builtin', 'transport') OR (proxy_id = 'profile.direct' AND proxy_revision = 1)),
`

func TestEgressSelectionRefusesTask1PrivateV2Unchanged(t *testing.T) {
	prior := strings.Replace(schemaV2SQL, egressSelectionColumns, "", 1)
	assertOldEgressSchemaRefusedUnchanged(t, prior, "70bec01ac198ebf108d295a23cc650b70174edb7f69c1c3bf42a1a33cce5bfba")
}

func TestEgressSelectionRefusesPreFixD3PrivateV2Unchanged(t *testing.T) {
	prior := strings.Replace(schemaV2SQL, egressSelectionColumns, priorD3EgressSelectionColumns, 1)
	assertOldEgressSchemaRefusedUnchanged(t, prior, "fd70fbdf0d95d2c8f4d5c2a992a218cc11b25b3ab54a900336441c3442fdbffd")
}

func assertOldEgressSchemaRefusedUnchanged(t *testing.T, prior, digest string) {
	t.Helper()
	if fmt.Sprintf("%x", sha256.Sum256([]byte(prior))) != digest {
		t.Fatal("prior-private fixture not exact Task1 schema")
	}
	path := t.TempDir() + "/runtime.db"
	db := openSQLiteForTest(t, path)
	nativeExec(t, db, prior)
	nativeExec(t, db, `INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',2,'2026-10-05T00:00:00Z')`)
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = InspectSchema(context.Background(), path, time.Second); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("unknown private v2 inspection accepted", err)
	}
	store, err := Open(context.Background(), Options{DatabasePath: path, BusyTimeout: DefaultBusyTimeout, CommitReconcileTimeout: DefaultCommitReconcileTimeout})
	if store != nil {
		shutdownTestStore(t, store)
		t.Fatal("old private v2 opened")
	}
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refusal changed old private v2")
	}
}

func TestEgressSelectionUpgradeLegacyAndConstraints(t *testing.T) {
	db := openSQLiteForTest(t, t.TempDir()+"/runtime.db")
	nativeExec(t, db, schemaV1SQL)
	nativeExec(t, db, `INSERT INTO runtime_egress_attempts(attempt_id,purpose,payload_class,parent_kind,parent_id,caller_kind,target_origin,policy_id,policy_revision,policy_authority,rule_id,proxy_id,proxy_revision,account_id,account_settings_revision,started_at_unix_ms) VALUES('legacy-egress','update','runtime','runtime_action','independent-action','core','https://legacy.example','legacy',1,'runtime','legacy','direct',0,'',0,1)`)
	nativeExec(t, db, migration001To002SQL)
	if err := migrationAdditionsEmpty(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	stored, err := scanEgressAttempt(db.QueryRow(egressAttemptSelect + ` WHERE attempt_id='legacy-egress'`))
	if err != nil || stored.Attempt.Decision().SelectionSource != "" || stored.Attempt.Decision().DefaultRevision != 0 {
		t.Fatal("legacy source invented", err)
	}
	for _, change := range []string{"selection_source='global'", "default_revision=1", "selection_source='unknown'", "selection_source='builtin',default_revision=1", "selection_source='account',default_revision=1", "selection_source='global',default_revision=-1"} {
		if _, err := db.Exec(`UPDATE runtime_egress_attempts SET ` + change + ` WHERE attempt_id='legacy-egress'`); err == nil {
			t.Fatal("invalid typed selection columns accepted", change)
		}
	}
	nativeExec(t, db, `UPDATE runtime_egress_attempts SET selection_source='global',default_revision=2,proxy_revision=1 WHERE attempt_id='legacy-egress'`)
	if err := migrationAdditionsEmpty(context.Background(), db); err == nil {
		t.Fatal("migration certification accepted changed new evidence columns")
	}
}
