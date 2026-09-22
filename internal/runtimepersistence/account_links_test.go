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

	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestAccountLinksPersistIndependentlyWithCAS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	ref, err := secretstore.ParseReference("secret://provider-account/independent")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1786200000, 0).UTC()
	account := provideraccount.Account{ID: "independent", DisplayName: "Independent", Origin: providerTestOrigin(t), RealmID: "anthropic.official", Driver: providerauth.AnthropicAPIKeyDriverRef(), SecretRef: ref, State: provideraccount.StateActive, Revision: 1, AssociationRevision: 1, CreatedAt: now, UpdatedAt: now}
	if result, err := store.ProviderAccountRepository().Write(ctx, 0, account); err != nil || result.Outcome != provideraccount.CommitCommitted {
		t.Fatalf("create: %+v %v", result, err)
	}
	account.Associations, err = provideraccount.NewEndpointAssociations([]upstreamendpoint.ID{"profile.first", "profile.second"})
	if err != nil {
		t.Fatal(err)
	}
	account.AssociationRevision = 2
	account.UpdatedAt = now.Add(time.Second)
	if result, err := store.ProviderAccountRepository().WriteAssociations(ctx, 1, account); err != nil || result.Outcome != provideraccount.CommitCommitted {
		t.Fatalf("link: %+v %v", result, err)
	}
	if result, err := store.ProviderAccountRepository().WriteAssociations(ctx, 1, account); err != nil || result.Outcome != provideraccount.CommitConflict || result.Actual != 2 {
		t.Fatalf("stale link: %+v %v", result, err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	t.Cleanup(func() { _ = reopened.Shutdown(ctx) })
	loaded, exists, err := reopened.ProviderAccountRepository().Load(ctx, account.ID)
	if err != nil || !exists || loaded != account {
		t.Fatalf("reopen: %+v %t %v", loaded, exists, err)
	}
	loaded.Associations = provideraccount.EndpointAssociations{}
	loaded.AssociationRevision++
	if result, err := reopened.ProviderAccountRepository().WriteAssociations(ctx, 2, loaded); err != nil || result.Outcome != provideraccount.CommitCommitted || result.Account.SecretRef != ref || result.Account.Revision != 1 {
		t.Fatalf("unlink: %+v %v", result, err)
	}
}

func TestOwnedDevelopmentAccountsBecomeLinksWithoutLosingCredentials(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	writeAnthropicEndpoint(t, store)
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(newSQLiteConnector(path, DefaultBusyTimeout))
	defer db.Close()
	// Reconstruct exactly the preceding development account table, preserving
	// the rest of the current schema and a real endpoint row.
	_, definition, _ := strings.Cut(schemaSQL, `CREATE TABLE "provider_accounts"(`)
	definition, _, _ = strings.Cut(definition, ") STRICT;")
	current := `CREATE TABLE "provider_accounts"(` + definition + ") STRICT;"
	prior := strings.Replace(current, "  credential_origin TEXT NOT NULL,\n  endpoint_associations TEXT NOT NULL CHECK(json_valid(endpoint_associations)),\n  association_revision INTEGER NOT NULL CHECK(association_revision BETWEEN 1 AND 9223372036854775807),", "  upstream_endpoint_id TEXT NOT NULL\n  REFERENCES upstream_endpoints(endpoint_id),", 1)
	prior = strings.Replace(prior, accountNoteColumnsSQL, "", 1)
	priorSchema := strings.Replace(schemaSQL, current, prior, 1)
	priorSchema = strings.ReplaceAll(priorSchema, "'upstream_account_read',\n", "")
	priorSchema = strings.ReplaceAll(priorSchema, "'credential_refresh',\n", "")
	priorSchema = strings.Replace(priorSchema, "CREATE INDEX provider_accounts_origin_state\nON provider_accounts(credential_origin, state, account_id);", "CREATE INDEX provider_accounts_endpoint_state\nON provider_accounts(upstream_endpoint_id, state, account_id);", 1)
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(priorSchema))); got != ownedAccountDevelopmentDigest {
		t.Fatalf("prior schema fixture digest=%s", got)
	}
	if _, err := db.Exec(`DROP TABLE provider_accounts;` + prior); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_accounts(account_id, display_name, upstream_endpoint_id, realm_id, driver_ref, secret_reference, state, revision, created_at_unix_ms, updated_at_unix_ms) VALUES(?, ?, ?, ?, ?, ?, 'active', 7, 1786200000000, 1786200001000)`, "preserved", "Preserved", upstreamendpoint.AnthropicOfficialID.String(), "anthropic.official", providerauth.AnthropicAPIKeyDriverRef().String(), "secret://provider-account/preserved"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runtime_metadata SET schema_source_sha256 = ? WHERE singleton = 1`, ownedAccountDevelopmentDigest); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	loaded, exists, err := reopened.ProviderAccountRepository().Load(ctx, "preserved")
	if err != nil || !exists || loaded.Revision != 7 || loaded.AssociationRevision != 1 || loaded.SecretRef.String() != "secret://provider-account/preserved" || loaded.Origin.String() != "https://api.anthropic.com" || !loaded.Associations.Contains(upstreamendpoint.AnthropicOfficialID) {
		t.Fatalf("preserve account: %+v %t %v", loaded, exists, err)
	}
	if err := reopened.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	final := openTestStore(t, path)
	t.Cleanup(func() { _ = final.Shutdown(ctx) })
	loadedAgain, exists, err := final.ProviderAccountRepository().Load(ctx, "preserved")
	if err != nil || !exists || loadedAgain != loaded {
		t.Fatalf("repeat open: %+v %t %v", loadedAgain, exists, err)
	}
}
