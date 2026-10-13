package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"strings"
	"testing"
	"time"
)

func TestNativeEffectiveEgressBindRemainsAtomicAcrossRealCommitFaults(t *testing.T) {
	for _, mode := range []string{"effective-insert-refusal", "rollback", "commit-then-error"} {
		t.Run(mode, func(t *testing.T) {
			store, baseID, _ := nativeLifecycleFixture(t, environment.ToolPolicyObserve)
			repo := store.NativeToolRepository()
			base, _ := nativeDecisionFixture(t, store, baseID)
			now := time.Now().UTC()
			catalog, _ := base.CatalogDigest()
			binding, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestPing, Plan: base.Plan(), Capture: base.Capture(), AssignmentRevision: base.AssignmentRevision(), OwnerEpoch: base.OwnerEpoch(), SessionID: base.SessionID(), SessionRevision: base.SessionRevision(), CatalogGeneration: base.CatalogGeneration(), CatalogDigest: &catalog, ExactRPC: []byte(`{"jsonrpc":"2.0","id":903,"method":"ping"}`)})
			if err != nil {
				t.Fatal(err)
			}
			id := nativetool.ID("93939393939393939393939393939393")
			nativeReceiveOnly(t, repo, id, binding, now)
			switch mode {
			case "effective-insert-refusal":
				if _, err := store.database.Exec(`CREATE TEMP TRIGGER reject_effective_fixture BEFORE INSERT ON native_tool_binding_egress BEGIN SELECT RAISE(ABORT,'synthetic effective insert refusal'); END`); err != nil {
					t.Fatal(err)
				}
			case "rollback":
				store.nativeTools.committer = rollbackThenError{}
			case "commit-then-error":
				store.nativeTools.committer = commitThenError{}
			}
			err = repo.Bind(t.Context(), id, binding, now)
			store.nativeTools.committer = sqlTransactionCommitter{}
			if mode == "effective-insert-refusal" {
				if _, dropErr := store.database.Exec(`DROP TRIGGER reject_effective_fixture`); dropErr != nil {
					t.Fatal(dropErr)
				}
			}
			if err == nil {
				t.Fatal("real Bind fault returned success")
			}
			if mode != "effective-insert-refusal" && !errors.Is(err, nativetool.ErrUnavailable) {
				t.Fatalf("commit fault not fenced as unavailable: %v", err)
			}
			want := 0
			if mode == "commit-then-error" {
				want = 1
			}
			for _, table := range []string{"native_tool_bindings", "native_tool_binding_egress"} {
				var count int
				if err := store.database.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE operation_id=?`, id).Scan(&count); err != nil || count != want {
					t.Fatalf("atomic %s count%d want%d err%v", table, count, want, err)
				}
			}
			op, err := repo.Get(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			wantState := "received"
			if want == 1 {
				wantState = "validated"
			}
			if op.State != wantState {
				t.Fatalf("atomic state %s want%s", op.State, wantState)
			}
			if countRows(t, store, "native_tool_once") != 0 || countRows(t, store, "runtime_egress_attempts") != 0 {
				t.Fatal("Bind failure consumed/send-audited despite absent Start")
			}
		})
	}
}

func TestNativeHistoricalV1BindingIsReadableButNeverLiveAuthority(t *testing.T) {
	store, id, authority := nativeLifecycleFixture(t, environment.ToolPolicyObserve)
	repo := store.NativeToolRepository()
	op, err := repo.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	b, err := loadNativeBinding(t.Context(), tx, id)
	if err != nil {
		t.Fatal(err)
	}
	env, exists, err := loadEnvironmentRevision(t.Context(), tx, op.EnvironmentID, op.EnvironmentRevision)
	if err != nil || !exists {
		t.Fatal(err)
	}
	var rpc []byte
	if err := tx.QueryRow(`SELECT rpc_id_digest FROM native_tool_inbound WHERE operation_id=?`, id).Scan(&rpc); err != nil {
		t.Fatal(err)
	}
	evidence := nativetool.BindingEvidence{Kind: op.Kind, Capture: op.Capture, Environment: op.EnvironmentID, EnvironmentRevision: op.EnvironmentRevision, EnvironmentDigest: op.EnvironmentDigest, Route: *env.ClientToolAccess.Route, Recording: op.Recording, Policy: env.EffectivePolicySet(), Assignment: b.assignment, Owner: b.owner, Session: b.session, SessionRevision: b.sessionRevision, CatalogGeneration: b.generation, Catalog: nativeStoredDigest(b.catalog), Parent: b.parent, Server: nativeStoredDigest(b.server), Request: b.request, Params: b.params, Group: b.group, Replay: nativeStoredDigest(b.replay), Name: b.name, RPCID: nativeStoredDigest(rpc), CancelledRPCID: nativeStoredDigest(b.cancel)}
	encoded, err := json.Marshal(struct {
		Domain string
		nativetool.BindingEvidence
	}{"native-binding/v1", evidence})
	if err != nil {
		t.Fatal(err)
	}
	v1 := nativetool.Digest(sha256.Sum256(encoded))
	if err := nativetool.VerifyBindingEvidence(evidence, v1); err != nil {
		t.Fatal("fixture is not genuine frozen V1 evidence", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// Historical metadata projection only. No session, assignment, issuer,
	// approval, once or audit is manufactured; all remain from the real fixture.
	var bindingTrigger, deleteTrigger string
	for name, out := range map[string]*string{"native_tool_binding_immutable": &bindingTrigger, "native_tool_binding_egress_delete_guard": &deleteTrigger} {
		if err := store.database.QueryRow(`SELECT sql FROM sqlite_schema WHERE name=?`, name).Scan(out); err != nil {
			t.Fatal(err)
		}
	}
	var profileJSON, source string
	var defaultRevision uint64
	if err := store.database.QueryRow(`SELECT profile_json,selection_source,default_revision FROM native_tool_binding_egress WHERE operation_id=?`, id).Scan(&profileJSON, &source, &defaultRevision); err != nil {
		t.Fatal(err)
	}
	mutate := func(historical bool) error {
		tx, err := store.database.BeginTx(t.Context(), nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, name := range []string{"native_tool_binding_immutable", "native_tool_binding_egress_delete_guard"} {
			if _, err := tx.Exec(`DROP TRIGGER ` + name); err != nil {
				return err
			}
		}
		digest := b.digest
		if historical {
			digest = v1
		}
		if historical {
			if _, err := tx.Exec(`DELETE FROM native_tool_binding_egress WHERE operation_id=?`, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE native_tool_bindings SET binding_digest=? WHERE operation_id=?`, digest[:], id); err != nil {
			return err
		}
		if !historical {
			var insertTrigger string
			if err := tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='native_tool_binding_egress_insert_guard'`).Scan(&insertTrigger); err != nil {
				return err
			}
			if _, err := tx.Exec(`DROP TRIGGER native_tool_binding_egress_insert_guard`); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO native_tool_binding_egress VALUES(?,?,2,?,?,?)`, id, digest[:], profileJSON, source, defaultRevision); err != nil {
				return err
			}
			if _, err := tx.Exec(insertTrigger); err != nil {
				return err
			}
		}
		for _, ddl := range []string{bindingTrigger, deleteTrigger} {
			if _, err := tx.Exec(ddl); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if err := mutate(true); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := mutate(false); err != nil {
			t.Error("restore synthetic historical metadata", err)
		}
	}()
	history, err := repo.Get(t.Context(), id)
	if err != nil || history.BindingDigest == nil || *history.BindingDigest != v1 {
		t.Fatalf("genuine V1 historical projection lost %+v err=%v", history, err)
	}
	if _, err := repo.Start(t.Context(), id, "historical-v1-must-not-send", authority, time.Now().UTC()); err == nil {
		t.Fatal("historical V1 issued live receipt")
	}
	if countRows(t, store, "native_tool_once") != 0 || countRows(t, store, "runtime_egress_attempts") != 0 {
		t.Fatal("historical read granted send effects")
	}
}

func TestNativeBindingPersistsEffectiveExitAndReconstructsAuditWithoutDefaultLookup(t *testing.T) {
	store, id, authority := nativeLifecycleFixture(t, environment.ToolPolicyObserve)
	repo := store.NativeToolRepository()
	op, err := repo.Get(t.Context(), id)
	if err != nil || op.State != "validated" {
		t.Fatalf("new effective binding cannot be read from real Store: state=%s err=%v", op.State, err)
	}
	receipt, err := repo.Start(t.Context(), id, "native-effective-attempt", authority, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var source, proxy string
	var defaultRevision, profileRevision uint64
	if err := store.database.QueryRowContext(t.Context(), `SELECT selection_source,default_revision,proxy_id,proxy_revision FROM runtime_egress_attempts WHERE attempt_id=?`, receipt.AttemptID()).Scan(&source, &defaultRevision, &proxy, &profileRevision); err != nil {
		t.Fatal(err)
	}
	decision := struct {
		SelectionSource egressprofile.SelectionSource
		DefaultRevision uint64
		ProxyID         string
		ProxyRevision   uint64
	}{egressprofile.SelectionSource(source), defaultRevision, proxy, profileRevision}
	if decision.SelectionSource != egressprofile.SelectionFlow || decision.DefaultRevision != 0 || decision.ProxyID != "profile.direct" || decision.ProxyRevision != 1 {
		t.Fatalf("audit lost immutable effective binding: %+v", decision)
	}
	if err := repo.VerifyStartReceipt(t.Context(), receipt, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(t.Context(), id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyStartReceipt(t.Context(), receipt, time.Now().UTC()); err == nil {
		t.Fatal("completed receipt became sendable")
	}
}

func TestNativeEffectiveEgressOwnedImmutableAndCaptureDeletionCascades(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyObserve)
	for _, query := range []string{
		`UPDATE native_tool_binding_egress SET default_revision=7 WHERE operation_id=?`,
		`DELETE FROM native_tool_binding_egress WHERE operation_id=?`,
	} {
		if _, err := store.database.ExecContext(t.Context(), query, id); err == nil {
			t.Fatal("owned immutable effective evidence changed while operation survives")
		}
	}
	op, err := store.NativeToolRepository().Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if countRows(t, store, "native_tool_binding_egress") != 4 {
		t.Fatal("binding evidence ownership missing")
	}
	if _, err = store.DeleteCapture(t.Context(), string(op.Capture.Kind), op.Capture.ID); err != nil {
		t.Fatal(err)
	}
	if countRows(t, store, "native_tool_binding_egress") != 0 {
		t.Fatal("Capture deletion retained effective rows")
	}
}

func TestNativeEffectiveEgressMissingOrCorruptNeverIssuesStart(t *testing.T) {
	for _, mutation := range []string{"missing", "wrong-profile", "wrong-source", "unknown-version", "duplicate-profile"} {
		t.Run(mutation, func(t *testing.T) {
			store, id, authority := nativeLifecycleFixture(t, environment.ToolPolicyObserve)
			var raw, source string
			var revision uint64
			var digest []byte
			if err := store.database.QueryRowContext(t.Context(), `SELECT binding_digest,profile_json,selection_source,default_revision FROM native_tool_binding_egress WHERE operation_id=?`, id).Scan(&digest, &raw, &source, &revision); err != nil {
				t.Fatal(err)
			}
			// CORRUPT SYNTHETIC EVIDENCE ONLY: no assignment, session, issuer,
			// approval or once authority is edited or manufactured. Restore before cleanup.
			for _, name := range []string{"native_tool_binding_egress_immutable", "native_tool_binding_egress_delete_guard", "native_tool_binding_egress_insert_guard"} {
				if _, err := store.database.ExecContext(t.Context(), `DROP TRIGGER `+name); err != nil {
					t.Fatal(err)
				}
			}
			defer func() {
				ctx := context.Background()
				store.database.ExecContext(ctx, `PRAGMA ignore_check_constraints=OFF`)
				if _, err := store.database.ExecContext(ctx, `DELETE FROM native_tool_binding_egress WHERE operation_id=?`, id); err != nil {
					t.Error(err)
					return
				}
				if _, err := store.database.ExecContext(ctx, `INSERT INTO native_tool_binding_egress VALUES(?,?,2,?,?,?)`, id, digest, raw, source, revision); err != nil {
					t.Error(err)
					return
				}
				schema := nativeSQL(t, "native_schema.sql")
				start := strings.Index(schema, "CREATE TRIGGER native_tool_binding_egress_insert_guard")
				end := strings.Index(schema[start:], "CREATE TABLE native_tool_approvals")
				if _, err := store.database.ExecContext(ctx, schema[start:start+end]); err != nil {
					t.Error(err)
				}
			}()
			var err error
			switch mutation {
			case "missing":
				_, err = store.database.ExecContext(t.Context(), `DELETE FROM native_tool_binding_egress WHERE operation_id=?`, id)
			case "wrong-profile":
				_, err = store.database.ExecContext(t.Context(), `UPDATE native_tool_binding_egress SET profile_json=? WHERE operation_id=?`, strings.Replace(raw, `"profile.direct"`, `"profile.foreign"`, 1), id)
			case "wrong-source":
				_, err = store.database.ExecContext(t.Context(), `UPDATE native_tool_binding_egress SET selection_source='global',default_revision=1 WHERE operation_id=?`, id)
			case "duplicate-profile":
				_, err = store.database.ExecContext(t.Context(), `UPDATE native_tool_binding_egress SET profile_json=? WHERE operation_id=?`, strings.Replace(raw, `"revision":1`, `"revision":1,"revision":1`, 1), id)
			case "unknown-version":
				if _, err = store.database.ExecContext(t.Context(), `PRAGMA ignore_check_constraints=ON`); err == nil {
					_, err = store.database.ExecContext(t.Context(), `UPDATE native_tool_binding_egress SET binding_version=3 WHERE operation_id=?`, id)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			repo := store.NativeToolRepository()
			if _, err := repo.Get(t.Context(), id); err == nil {
				t.Fatal("corrupt or missing V2 evidence became readable V1")
			}
			if _, err := repo.Start(t.Context(), id, "corrupt-egress-attempt", authority, time.Now().UTC()); err == nil {
				t.Fatal("corrupt effective evidence issued real receipt")
			}
			if countRows(t, store, "native_tool_once") != 0 || countRows(t, store, "runtime_egress_attempts") != 0 {
				t.Fatal("denied corruption consumed once or created audit")
			}
		})
	}
}
