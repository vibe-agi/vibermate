package runtimepersistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

const selectRedactionSalt = `
  SELECT salt FROM runtime_raw_evidence_redaction WHERE singleton = 1`

// RedactionSalt returns this database's stable redaction salt, creating it on
// first use.
//
// The salt is not a secret and is deliberately not a SecretRef.
// INV-STORE-DISCLOSED already states this database is not protected at rest, so
// a salt stored beside the digests it keys cannot defend against an adversary
// holding the file. What it does prevent is a redacted digest being matched
// against a corpus assembled elsewhere, which matters for low-entropy values
// such as Proxy-Authorization: Basic.
func (repository *rawEvidenceRepository) RedactionSalt(
	ctx context.Context,
) ([]byte, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return nil, fmt.Errorf("begin raw evidence redaction salt: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	salt, err := readRedactionSalt(operation, transaction)
	if err == nil {
		return salt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	created := make([]byte, rawevidence.RedactionSaltBytes)
	if _, err := rand.Read(created); err != nil {
		return nil, fmt.Errorf("create raw evidence redaction salt: %w", err)
	}
	if _, err := transaction.ExecContext(
		operation,
		`INSERT INTO runtime_raw_evidence_redaction(singleton, salt)
		 VALUES (1, ?) ON CONFLICT(singleton) DO NOTHING`,
		created,
	); err != nil {
		return nil, fmt.Errorf("persist raw evidence redaction salt: %w", err)
	}
	// Re-read rather than returning the generated value: a concurrent creator
	// may have won, and the stored row is the only authority.
	salt, err = readRedactionSalt(operation, transaction)
	if err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit raw evidence redaction salt: %w", err)
	}
	return salt, nil
}

func readRedactionSalt(
	ctx context.Context,
	transaction *sql.Tx,
) ([]byte, error) {
	var salt []byte
	if err := transaction.QueryRowContext(
		ctx, selectRedactionSalt,
	).Scan(&salt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read raw evidence redaction salt: %w", err)
	}
	if len(salt) != rawevidence.RedactionSaltBytes {
		return nil, errors.New("stored raw evidence redaction salt is invalid")
	}
	return slices.Clone(salt), nil
}

// encodeRedactedCredentialFields renders the removed field names for the safe
// metadata column. The names are canonical header keys, so encoding a []string
// cannot fail.
func encodeRedactedCredentialFields(names []string) string {
	if len(names) == 0 {
		return "[]"
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func decodeRedactedCredentialFields(encoded string) ([]string, error) {
	if encoded == "" || encoded == "[]" {
		return nil, nil
	}
	var names []string
	if err := json.Unmarshal([]byte(encoded), &names); err != nil {
		return nil, fmt.Errorf("decode redacted credential fields: %w", err)
	}
	if len(names) == 0 {
		return nil, nil
	}
	return names, nil
}
