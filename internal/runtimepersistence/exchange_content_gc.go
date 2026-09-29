package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// deleteExchangeContent follows only the references released by this deletion.
// Each probe uses a foreign-key index; unrelated retained graphs are not read.
func deleteExchangeContent(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	rows, err := tx.QueryContext(ctx, query+` RETURNING request_transcript_digest,
	 expected_transcript_digest, coalesce(base_transcript_digest,''),
	 coalesce(response_message_digest,''), coalesce(system_message_digest,'')`, args...)
	if err != nil {
		return 0, fmt.Errorf("delete Exchange content: %w", err)
	}
	defer rows.Close()
	var roots, messages []string
	var count int64
	for rows.Next() {
		var request, expected, base, response, system string
		if err := rows.Scan(&request, &expected, &base, &response, &system); err != nil {
			return 0, err
		}
		roots = append(roots, request, expected, base)
		messages = append(messages, response, system)
		count++
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	for len(roots) > 0 {
		digest := roots[len(roots)-1]
		roots = roots[:len(roots)-1]
		if digest == "" {
			continue
		}
		var parent, message string
		err := tx.QueryRowContext(ctx, `DELETE FROM runtime_exchange_content_transcripts
		 WHERE digest=?
		 AND NOT EXISTS(SELECT 1 FROM runtime_exchange_content_transcripts WHERE parent_digest=?)
		 AND NOT EXISTS(SELECT 1 FROM runtime_exchange_contents WHERE request_transcript_digest=?)
		 AND NOT EXISTS(SELECT 1 FROM runtime_exchange_contents WHERE expected_transcript_digest=?)
		 AND NOT EXISTS(SELECT 1 FROM runtime_exchange_contents WHERE base_transcript_digest=?)
		 RETURNING coalesce(parent_digest,''), message_digest`, digest, digest, digest, digest, digest).Scan(&parent, &message)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("release transcript node: %w", err)
		}
		// Retry a parent after removing its last child, even if an earlier probe
		// found it still referenced. A global visited set would leak ancestors.
		roots = append(roots, parent)
		messages = append(messages, message)
	}
	for _, digest := range uniqueStrings(messages) {
		if digest == "" {
			continue
		}
		var manifest string
		err := tx.QueryRowContext(ctx, `DELETE FROM runtime_exchange_content_messages
		 WHERE digest=?
		 AND NOT EXISTS(SELECT 1 FROM runtime_exchange_content_transcripts WHERE message_digest=?)
		 AND NOT EXISTS(SELECT 1 FROM runtime_exchange_contents WHERE response_message_digest=?)
		 AND NOT EXISTS(SELECT 1 FROM runtime_exchange_contents WHERE system_message_digest=?)
		 RETURNING block_manifest`, digest, digest, digest, digest).Scan(&manifest)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("release transcript message: %w", err)
		}
		if err := purgeUnreferencedContentBlocks(ctx, tx, manifest); err != nil {
			return 0, err
		}
	}
	return count, nil
}
