package runtimepersistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// rememberConnectionAnswer writes the rule a remembered answer means, inside
// the transaction that resolved the question.
//
// The rule is exactly as wide as what was asked: this host on this port, and
// nothing else. A person who wants something wider writes that rule
// themselves, which is what design 06 requires of a global allow.
func rememberConnectionAnswer(
	ctx context.Context,
	transaction *sql.Tx,
	record toolapproval.Record,
	now time.Time,
) error {
	if record.DecisionScope != toolapproval.ScopeHostPort {
		return nil
	}
	if record.Kind != toolapproval.KindNetworkAsk {
		return toolapproval.ErrInvalidApproval
	}
	decision := connectionpolicy.DecisionAllow
	if record.Decision == toolapproval.DecisionDeny {
		decision = connectionpolicy.DecisionDeny
	}
	identifier, err := rememberedRuleID()
	if err != nil {
		return err
	}
	// A remembered rule outranks the shipped placeholder, which matches
	// everything at the lowest precedence, without outranking anything a
	// person set deliberately higher.
	const rememberedPriority = 100
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO connection_rules (
		     rule_id, is_default, priority, decision,
		     match_kind, match_host, match_port
		 ) VALUES (?, 0, ?, ?, 'exact_host_port', ?, ?)
		 ON CONFLICT (match_kind, match_host, match_port)
		     WHERE is_default = 0
		 DO UPDATE SET decision = excluded.decision,
		               priority = excluded.priority`,
		identifier,
		rememberedPriority,
		string(decision),
		record.Target.Host,
		int64(record.Target.Port),
	); err != nil {
		return fmt.Errorf("remember connection answer: %w", err)
	}
	// The set carries one revision, so a reader can tell that the rules it
	// holds are no longer the rules in force.
	result, err := transaction.ExecContext(
		ctx,
		`UPDATE connection_rule_sets
		    SET revision = revision + 1,
		        updated_at_unix_ms = ?
		  WHERE id = 1`,
		toUnixMillis(now),
	)
	if err != nil {
		return fmt.Errorf("advance connection rule revision: %w", err)
	}
	// A rule written into a set that does not exist would be a rule nothing
	// evaluates. The whole decision fails rather than resolving a question
	// whose answer went nowhere.
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf(
			"%w: there is no connection rule set to remember into",
			connectionpolicy.ErrInvalidRuleSet,
		)
	}
	return nil
}

func rememberedRuleID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate remembered rule ID: %w", err)
	}
	return "remembered." + base64.RawURLEncoding.EncodeToString(raw), nil
}
