package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

func TestPublished013AuditUpgradesWithoutLosingRowsOrSequence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	oldSchema := strings.Replace(schemaSQL, "'upstream_account_action',\n", "", 1)
	if fmt.Sprintf("%x", sha256.Sum256([]byte(oldSchema))) != published013SchemaDigest {
		t.Fatal("published v0.1.13 schema fixture drifted")
	}
	old := sql.OpenDB(newSQLiteConnector(path, DefaultBusyTimeout))
	if _, err := old.ExecContext(ctx, oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(ctx, `INSERT INTO runtime_metadata(singleton, schema_identity, schema_revision, schema_source_sha256, initialized_at) VALUES (1, ?, ?, ?, ?)`, currentSchemaIdentity, currentSchemaRevision, published013SchemaDigest, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	previous, err := newEgressAttemptRepository(old, newOperationGate()).Append(ctx, providerAttempt(t, "released-old"))
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	current := openTestStore(t, path)
	t.Cleanup(func() { shutdownTestStore(t, current) })
	page, err := current.EgressAttemptRepository().List(ctx, egressaudit.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Attempt.ID() != "released-old" || page.Items[0].Sequence != previous.Sequence {
		t.Fatalf("published egress row changed during upgrade: %+v, %v", page.Items, err)
	}
	input := egressaudit.NewInput{
		ID: "new-reset", Purpose: egressaudit.PurposeUpstreamAccountAction,
		PayloadClass: egressaudit.PayloadRuntime,
		Parent:       egressaudit.ParentRef{Kind: egressaudit.ParentRuntimeAction, ID: "owner-reset"},
		Caller:       egressaudit.CallerCore, TargetOrigin: "https://chatgpt.com",
		Decision:  egressaudit.BuiltInDirectDecision(egressaudit.AuthorityRuntime),
		StartedAt: time.Now().UTC(),
	}
	action, err := egressaudit.New(input)
	if err != nil {
		t.Fatal(err)
	}
	next, err := current.EgressAttemptRepository().Append(ctx, action)
	if err != nil || next.Sequence != previous.Sequence+1 {
		t.Fatalf("new purpose or sequence was lost: %+v, %v", next, err)
	}
	state, err := current.SchemaStateReader().ReadSchemaState(ctx)
	if err != nil || state.SourceSHA256 == published013SchemaDigest {
		t.Fatalf("released schema binding was not advanced: %+v, %v", state, err)
	}
}
