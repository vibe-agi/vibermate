package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/codelibrary"
)

type codeLibraryRepository struct {
	database         *sql.DB
	operations       *operationGate
	reconcileTimeout time.Duration
	committer        transactionCommitter
}

var _ codelibrary.Repository = (*codeLibraryRepository)(nil)

func newCodeLibraryRepository(
	database *sql.DB,
	operations *operationGate,
	reconcileTimeout time.Duration,
	committer transactionCommitter,
) *codeLibraryRepository {
	return &codeLibraryRepository{
		database: database, operations: operations,
		reconcileTimeout: reconcileTimeout, committer: committer,
	}
}

func (repository *codeLibraryRepository) CreateCollection(
	ctx context.Context,
	collection codelibrary.Collection,
) error {
	if collection.Validate() != nil {
		return codelibrary.ErrInvalidLibrary
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return err
	}
	defer permit.finish()
	result, err := repository.database.ExecContext(
		permit.context,
		`INSERT INTO code_library_collections(collection_id, display_name)
		 VALUES (?, ?) ON CONFLICT(collection_id) DO NOTHING`,
		collection.ID.String(),
		collection.DisplayName,
	)
	if err != nil {
		return fmt.Errorf("create Code Library collection: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return codelibrary.ErrRevisionConflict
	}
	return nil
}

func (repository *codeLibraryRepository) WriteTransform(
	ctx context.Context,
	expected codelibrary.Revision,
	candidate codelibrary.TransformRevision,
) (codelibrary.CommitResult, error) {
	if candidate.Validate() != nil || expected >= codelibrary.MaxRevision ||
		candidate.Revision != expected+1 {
		return codelibrary.CommitResult{}, codelibrary.ErrInvalidLibrary
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return codelibrary.CommitResult{}, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return codelibrary.CommitResult{}, fmt.Errorf("begin Code Library publish: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	var collectionExists int
	if err := transaction.QueryRowContext(
		permit.context,
		`SELECT 1 FROM code_library_collections WHERE collection_id = ?`,
		candidate.CollectionID.String(),
	).Scan(&collectionExists); err != nil {
		if err == sql.ErrNoRows {
			return codelibrary.CommitResult{}, codelibrary.ErrCollectionNotFound
		}
		return codelibrary.CommitResult{}, fmt.Errorf("read Code Library collection: %w", err)
	}
	if _, err := transaction.ExecContext(
		permit.context,
		`INSERT INTO code_library_transform_heads(transform_id, current_revision)
		 VALUES (?, 0) ON CONFLICT(transform_id) DO NOTHING`,
		candidate.ID.String(),
	); err != nil {
		return codelibrary.CommitResult{}, fmt.Errorf("create Code Library Transform head: %w", err)
	}
	result, err := transaction.ExecContext(
		permit.context,
		`UPDATE code_library_transform_heads SET current_revision = ?
		 WHERE transform_id = ? AND current_revision = ?`,
		int64(candidate.Revision),
		candidate.ID.String(),
		int64(expected),
	)
	if err != nil {
		return codelibrary.CommitResult{}, fmt.Errorf("advance Code Library Transform: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return codelibrary.CommitResult{}, err
	}
	if affected != 1 {
		var actual int64
		_ = transaction.QueryRowContext(
			permit.context,
			`SELECT current_revision FROM code_library_transform_heads WHERE transform_id = ?`,
			candidate.ID.String(),
		).Scan(&actual)
		return codelibrary.CommitResult{
			Outcome: codelibrary.CommitConflict, ActualRevision: codelibrary.Revision(actual),
		}, nil
	}
	if _, err := transaction.ExecContext(
		permit.context,
		`INSERT INTO code_library_transform_revisions(
		   transform_id, revision, collection_id, display_name,
		   request_javascript, response_javascript, published_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		candidate.ID.String(),
		int64(candidate.Revision),
		candidate.CollectionID.String(),
		candidate.DisplayName,
		candidate.Policy.RequestJavaScript,
		candidate.Policy.ResponseJavaScript,
		toUnixMillis(candidate.PublishedAt),
	); err != nil {
		return codelibrary.CommitResult{}, fmt.Errorf("write Code Library Transform revision: %w", err)
	}
	commitErr := repository.committer.Commit(transaction)
	if commitErr == nil {
		return codelibrary.CommitResult{
			Outcome: codelibrary.CommitCommitted, Revision: candidate,
		}, nil
	}
	_ = transaction.Rollback()
	reconcileContext, cancel := context.WithTimeout(
		permit.ownerContext,
		repository.reconcileTimeout,
	)
	defer cancel()
	stored, exists, reconcileErr := loadCodeLibraryTransformRevision(
		reconcileContext,
		repository.database,
		candidate.ID,
		candidate.Revision,
	)
	actual, headExists, headErr := loadCodeLibraryTransformHead(
		reconcileContext,
		repository.database,
		candidate.ID,
	)
	if reconcileErr != nil || headErr != nil {
		return codelibrary.CommitResult{Outcome: codelibrary.CommitIndeterminate},
			errors.Join(commitErr, reconcileErr, headErr)
	}
	if exists && headExists && actual == candidate.Revision && stored.Equal(candidate) {
		return codelibrary.CommitResult{
			Outcome: codelibrary.CommitCommitted, Revision: stored, ActualRevision: actual,
		}, nil
	}
	if !exists && ((!headExists && expected == 0) || (headExists && actual == expected)) {
		return codelibrary.CommitResult{
			Outcome: codelibrary.CommitNotCommitted, ActualRevision: actual,
		}, commitErr
	}
	return codelibrary.CommitResult{
		Outcome: codelibrary.CommitIndeterminate, Revision: stored, ActualRevision: actual,
	}, commitErr
}

func (repository *codeLibraryRepository) LoadTransformRevision(
	ctx context.Context,
	id codelibrary.TransformID,
	revision codelibrary.Revision,
) (codelibrary.TransformRevision, bool, error) {
	parsed, err := codelibrary.NewTransformID(id.String())
	if err != nil || parsed != id || revision == 0 || revision > codelibrary.MaxRevision {
		return codelibrary.TransformRevision{}, false, codelibrary.ErrInvalidLibrary
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return codelibrary.TransformRevision{}, false, err
	}
	defer permit.finish()
	return loadCodeLibraryTransformRevision(permit.context, repository.database, id, revision)
}

func (repository *codeLibraryRepository) LoadCurrent(
	ctx context.Context,
) (codelibrary.Catalog, error) {
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return codelibrary.Catalog{}, err
	}
	defer permit.finish()
	catalog := codelibrary.Catalog{
		Collections: []codelibrary.Collection{},
		Transforms:  []codelibrary.TransformRevision{},
	}
	rows, err := repository.database.QueryContext(
		permit.context,
		`SELECT collection_id, display_name
		 FROM code_library_collections ORDER BY collection_id`,
	)
	if err != nil {
		return codelibrary.Catalog{}, fmt.Errorf("list Code Library collections: %w", err)
	}
	for rows.Next() {
		var id, displayName string
		if err := rows.Scan(&id, &displayName); err != nil {
			_ = rows.Close()
			return codelibrary.Catalog{}, err
		}
		collection := codelibrary.Collection{
			ID: codelibrary.CollectionID(id), DisplayName: displayName,
		}
		if collection.Validate() != nil {
			_ = rows.Close()
			return codelibrary.Catalog{}, codelibrary.ErrInvalidLibrary
		}
		catalog.Collections = append(catalog.Collections, collection)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return codelibrary.Catalog{}, err
	}
	if err := rows.Close(); err != nil {
		return codelibrary.Catalog{}, err
	}
	rows, err = repository.database.QueryContext(
		permit.context,
		`SELECT revisions.transform_id, revisions.revision, revisions.collection_id,
		        revisions.display_name, revisions.request_javascript,
		        revisions.response_javascript, revisions.published_at_unix_ms
		 FROM code_library_transform_heads AS heads
		 JOIN code_library_transform_revisions AS revisions
		   ON revisions.transform_id = heads.transform_id
		  AND revisions.revision = heads.current_revision
		 ORDER BY revisions.transform_id`,
	)
	if err != nil {
		return codelibrary.Catalog{}, fmt.Errorf("list Code Library Transforms: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		value, scanErr := scanCodeLibraryTransformRevision(rows)
		if scanErr != nil {
			return codelibrary.Catalog{}, scanErr
		}
		catalog.Transforms = append(catalog.Transforms, value)
	}
	if err := rows.Err(); err != nil {
		return codelibrary.Catalog{}, err
	}
	return catalog, nil
}

func loadCodeLibraryTransformRevision(
	ctx context.Context,
	query interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	id codelibrary.TransformID,
	revision codelibrary.Revision,
) (codelibrary.TransformRevision, bool, error) {
	value, err := scanCodeLibraryTransformRevision(query.QueryRowContext(
		ctx,
		`SELECT transform_id, revision, collection_id, display_name,
		        request_javascript, response_javascript, published_at_unix_ms
		 FROM code_library_transform_revisions
		 WHERE transform_id = ? AND revision = ?`,
		id.String(),
		int64(revision),
	))
	if err == sql.ErrNoRows {
		return codelibrary.TransformRevision{}, false, nil
	}
	if err != nil {
		return codelibrary.TransformRevision{}, false,
			fmt.Errorf("read Code Library Transform revision: %w", err)
	}
	if value.ID != id || value.Revision != revision {
		return codelibrary.TransformRevision{}, false, codelibrary.ErrInvalidLibrary
	}
	return value, true, nil
}

type codeLibraryRevisionRow interface {
	Scan(...any) error
}

func scanCodeLibraryTransformRevision(
	row codeLibraryRevisionRow,
) (codelibrary.TransformRevision, error) {
	var value codelibrary.TransformRevision
	var transformID, collectionID string
	var storedRevision, publishedAt int64
	err := row.Scan(
		&transformID,
		&storedRevision,
		&collectionID,
		&value.DisplayName,
		&value.Policy.RequestJavaScript,
		&value.Policy.ResponseJavaScript,
		&publishedAt,
	)
	if err != nil {
		return codelibrary.TransformRevision{}, err
	}
	value.ID = codelibrary.TransformID(transformID)
	value.Revision = codelibrary.Revision(storedRevision)
	value.CollectionID = codelibrary.CollectionID(collectionID)
	value.PublishedAt = fromUnixMillis(publishedAt)
	if value.Validate() != nil {
		return codelibrary.TransformRevision{}, codelibrary.ErrInvalidLibrary
	}
	return value, nil
}

func loadCodeLibraryTransformHead(
	ctx context.Context,
	query interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	id codelibrary.TransformID,
) (codelibrary.Revision, bool, error) {
	var revision int64
	err := query.QueryRowContext(
		ctx,
		`SELECT current_revision FROM code_library_transform_heads WHERE transform_id = ?`,
		id.String(),
	).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if revision <= 0 || revision > int64(codelibrary.MaxRevision) {
		return 0, false, codelibrary.ErrInvalidLibrary
	}
	return codelibrary.Revision(revision), true, nil
}
