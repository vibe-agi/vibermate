package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

// Every role influencing the group is proved by the whole canonical message,
// including skipped assistant messages. Proof shares the preview's message and
// byte limits; an unprovable role has a neutral explicit-loading entry instead.
func (repository *exchangeContentRepository) readingUserGroup(ctx context.Context, ref storedContentReference, ledger *storedReadLedger) ([]exchangecontent.Message, int, string, error) {
	messages := []exchangecontent.Message{}
	offset, anchor := ref.requestCount, ""
	budget := exchangecontent.PageContentBytes
	wantDigest := ref.requestRoot
	for depth := ref.requestCount; depth > 0; depth-- {
		var node storedTranscriptNode
		var parent sql.NullString
		err := repository.reads.QueryRowContext(ctx, `SELECT digest,parent_digest,message_digest,depth
		 FROM runtime_exchange_content_transcripts WHERE digest=?`, wantDigest).Scan(&node.digest, &parent, &node.messageDigest, &node.depth)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, "", exchangecontent.ErrInvalidEvidence
		}
		if err != nil {
			return nil, 0, "", err
		}
		if parent.Valid {
			value := parent.String
			node.parentDigest = &value
		}
		if err := validateContentNode(node, wantDigest, depth); err != nil {
			return nil, 0, "", err
		}
		cursor := pageCursor(ref, "request", "message", depth)
		if ref.requestCount-depth == exchangecontent.PageMessageLimit {
			// We have verified the latest 24 positions. If a group was found,
			// its bounded suffix is sufficient; otherwise role discovery needs
			// explicit loading at this next, ancestry-verified position.
			if len(messages) > 0 {
				break
			}
			return []exchangecontent.Message{{Role: "unknown", Blocks: []exchangecontent.Block{deferredPageBlock(cursor, 0)}}}, depth - 1, "", nil
		}
		message, used, err := repository.readingPageMessage(ctx, node.messageDigest, cursor, budget, ref, ledger)
		if err != nil {
			return nil, 0, "", err
		}
		if len(message.Blocks) == 1 && message.Blocks[0].Deferred != nil {
			// No SQL role may classify or skip this message, or establish a
			// group boundary. Discard any partial group rather than claim one.
			return []exchangecontent.Message{message}, depth - 1, "", nil
		}
		budget -= used
		if message.Role == "user" {
			if anchor == "" {
				anchor = node.digest
			}
			messages = append(messages, message)
			offset = depth - 1
		} else if len(messages) > 0 {
			break
		}
		wantDigest = parent.String
	}
	slices.Reverse(messages)
	return messages, offset, anchor, nil
}

// Reading role proof uses the original exact 128 KiB message debit. The
// physical preflight rejects corrupt sizes before the bounded decoder fetches
// a payload; the conservative general page estimate would hide a valid group
// at the exact boundary.
func (repository *exchangeContentRepository) readingPageMessage(ctx context.Context, digest string, cursor contentCursor, budget int, ref storedContentReference, ledger *storedReadLedger) (exchangecontent.Message, int, error) {
	size, count, err := repository.inlineMessageSize(ctx, digest, exchangecontent.PageMessageLimit)
	if err != nil {
		return exchangecontent.Message{}, 0, err
	}
	if size > budget || count > exchangecontent.PageMessageLimit {
		if ledger != nil {
			if err := ledger.holdShell(); err != nil {
				return exchangecontent.Message{}, 0, err
			}
		}
		return exchangecontent.Message{Role: "unknown", Blocks: []exchangecontent.Block{deferredPageBlock(cursor, min(size, exchangecontent.MaxEncodedBytes))}}, 1024, nil
	}
	if ledger == nil {
		return repository.inlinePageMessage(ctx, digest, cursor, budget, ref.manifest.Mode, nil, 7)
	}
	message, err := repository.readVerifiedMessage(ctx, digest, ref.manifest.Mode, ledger, true)
	if err != nil {
		return exchangecontent.Message{}, 0, err
	}
	return message, size, nil
}
