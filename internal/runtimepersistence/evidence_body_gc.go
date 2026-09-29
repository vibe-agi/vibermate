package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Only bodies named by deleted envelopes can have become unreachable. Their
// reverse chunk index keeps this independent of the amount of retained traffic.
func deleteRawEvidence(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	rows, err := tx.QueryContext(ctx, query+` RETURNING stored_body_digest`, args...)
	if err != nil {
		return 0, fmt.Errorf("delete raw evidence: %w", err)
	}
	defer rows.Close()
	var bodies []string
	var count int64
	for rows.Next() {
		var digest []byte
		if err := rows.Scan(&digest); err != nil {
			return 0, err
		}
		if len(digest) != 0 {
			bodies = append(bodies, string(digest))
		}
		count++
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	for _, digest := range uniqueStrings(bodies) {
		var manifest []byte
		err := tx.QueryRowContext(ctx, `DELETE FROM runtime_evidence_bodies
		 WHERE digest=? AND NOT EXISTS(SELECT 1 FROM runtime_raw_evidence_envelopes WHERE stored_body_digest=?)
		 RETURNING chunk_manifest`, []byte(digest), []byte(digest)).Scan(&manifest)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("release evidence body: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `WITH RECURSIVE spans(position) AS (
		 VALUES(1) UNION ALL SELECT position+32 FROM spans WHERE position+32<=length(?)
		) DELETE FROM runtime_evidence_chunks
		 WHERE digest IN(SELECT substr(?,position,32) FROM spans)
		 AND NOT EXISTS(SELECT 1 FROM runtime_evidence_chunk_refs WHERE chunk_digest=runtime_evidence_chunks.digest)`,
			manifest, manifest); err != nil {
			return 0, fmt.Errorf("release evidence chunks: %w", err)
		}
	}
	return count, nil
}
