package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

func TestCodeLibraryReopensEveryPublishedTransformRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	manager, err := codelibrary.NewManager(store.CodeLibraryRepository(), nil)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := manager.CreateCollection(ctx, codelibrary.CreateCollectionCommand{
		ID: "privacy", DisplayName: "Privacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.PublishTransform(ctx, codelibrary.PublishTransformCommand{
		ID: "redact-home", CollectionID: collection.ID, DisplayName: "Redact home",
		Policy: messagetransform.Policy{RequestJavaScript: `request.body += "-v1";`},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.PublishTransform(ctx, codelibrary.PublishTransformCommand{
		ID: "redact-home", CollectionID: collection.ID, DisplayName: "Redact home",
		ExpectedRevision: first.Revision,
		Policy:           messagetransform.Policy{RequestJavaScript: `request.body += "-v2";`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	defer shutdownTestStore(t, reopened)
	recovered, err := codelibrary.NewManager(reopened.CodeLibraryRepository(), nil)
	if err != nil {
		t.Fatal(err)
	}
	old, err := recovered.GetTransformRevision(ctx, first.ID, first.Revision)
	if err != nil {
		t.Fatalf("GetTransformRevision(first) error = %v", err)
	}
	latest, err := recovered.GetTransformRevision(ctx, second.ID, second.Revision)
	if err != nil {
		t.Fatalf("GetTransformRevision(second) error = %v", err)
	}
	if old.Policy.RequestJavaScript != `request.body += "-v1";` ||
		latest.Policy.RequestJavaScript != `request.body += "-v2";` {
		t.Fatalf("reopened revisions = %#v, %#v", old.Policy, latest.Policy)
	}
	if !old.PublishedAt.Equal(first.PublishedAt) || !latest.PublishedAt.Equal(second.PublishedAt) {
		t.Fatalf(
			"published timestamps changed across reopen: first=%s/%s second=%s/%s",
			first.PublishedAt, old.PublishedAt, second.PublishedAt, latest.PublishedAt,
		)
	}
	catalog, err := recovered.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(catalog.Collections) != 1 || len(catalog.Transforms) != 1 ||
		!catalog.Transforms[0].Equal(latest) {
		t.Fatalf("reopened current catalog = %+v, want revision 2 only", catalog)
	}
}

func TestCodeLibraryCommitErrorIsReconciled(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		committer transactionCommitter
		committed bool
	}{
		{name: "commit then error", committer: commitThenError{}, committed: true},
		{name: "rollback then error", committer: rollbackThenError{}, committed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
			defer shutdownTestStore(t, store)
			manager, err := codelibrary.NewManager(store.CodeLibraryRepository(), nil)
			if err != nil {
				t.Fatal(err)
			}
			collection, err := manager.CreateCollection(ctx, codelibrary.CreateCollectionCommand{
				ID: "examples", DisplayName: "Examples",
			})
			if err != nil {
				t.Fatal(err)
			}
			store.codeLibrary.committer = test.committer
			published, publishErr := manager.PublishTransform(ctx, codelibrary.PublishTransformCommand{
				ID: "timestamp", CollectionID: collection.ID, DisplayName: "Timestamp",
				Policy: messagetransform.Policy{ResponseJavaScript: `response.body += "now";`},
			})
			if test.committed {
				if publishErr != nil || published.Revision != 1 {
					t.Fatalf("PublishTransform() = %+v, %v; want reconciled revision 1", published, publishErr)
				}
				return
			}
			if publishErr == nil {
				t.Fatal("rolled-back publish unexpectedly succeeded")
			}
			_, getErr := manager.GetTransformRevision(ctx, "timestamp", 1)
			if !errors.Is(getErr, codelibrary.ErrTransformNotFound) {
				t.Fatalf("GetTransformRevision() error = %v, want ErrTransformNotFound", getErr)
			}
		})
	}
}
