package runtimepersistence

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

// Only metadata is walked through earlier history. The bounded user group is
// hydrated, without declaring it a new turn or altering retained evidence.
func (repository *exchangeContentRepository) readingUserGroup(ctx context.Context, ref storedContentReference) ([]exchangecontent.Message, int, string, error) {
	rows, err := repository.database.QueryContext(ctx, `WITH RECURSIVE chain(digest,parent_digest,message_digest,depth,role,seen_user) AS (
 SELECT t.digest,t.parent_digest,t.message_digest,t.depth,m.role,m.role='user'
 FROM runtime_exchange_content_transcripts t JOIN runtime_exchange_content_messages m ON m.digest=t.message_digest WHERE t.digest=?
 UNION ALL
 SELECT t.digest,t.parent_digest,t.message_digest,t.depth,m.role,chain.seen_user OR m.role='user'
 FROM runtime_exchange_content_transcripts t JOIN chain ON t.digest=chain.parent_digest
 JOIN runtime_exchange_content_messages m ON m.digest=t.message_digest
 WHERE NOT(chain.seen_user AND chain.role<>'user')
 ) SELECT digest,message_digest,depth FROM chain WHERE role='user' ORDER BY depth DESC LIMIT ?`, ref.requestRoot, exchangecontent.PageMessageLimit)
	if err != nil {
		return nil, 0, "", err
	}
	type userRef struct {
		node, message string
		depth         int
	}
	var refs []userRef
	for rows.Next() {
		var item userRef
		if err := rows.Scan(&item.node, &item.message, &item.depth); err != nil {
			_ = rows.Close()
			return nil, 0, "", err
		}
		refs = append(refs, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, 0, "", err
	}
	if err := rows.Close(); err != nil {
		return nil, 0, "", err
	}
	messages := []exchangecontent.Message{}
	if len(refs) == 0 {
		return messages, ref.requestCount, "", nil
	}
	budget := exchangecontent.PageContentBytes
	// Return oldest-to-newest within this user group, as in the original wire.
	for i := len(refs) - 1; i >= 0; i-- {
		item := refs[i]
		message, used, err := repository.inlinePageMessage(ctx, item.message, pageCursor(ref, "request", "message", item.depth), budget)
		if err != nil {
			return nil, 0, "", err
		}
		messages = append(messages, message)
		budget = max(0, budget-used)
	}
	return messages, refs[len(refs)-1].depth - 1, refs[0].node, nil
}
