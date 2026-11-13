package runtimepersistence

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
)

// SummarizeScripts says what message scripts did to the requests recorded
// since q.Since. It reads through the same visibility and copy rules as the
// request list, so deleted sessions and superseded copies are never counted.
//
// The request table has no time index, so the read is bounded by sequence: it
// examines at most the newest MaximumScriptActivityRequests requests of each
// source. When that bound is reached inside the window, the result says from
// when it actually covers.
func (r *ActivityInspectionReader) SummarizeScripts(ctx context.Context, q activity.ScriptActivityQuery) (resultValue activity.ScriptActivity, resultErr error) {
	if r != nil {
		defer func(caller context.Context) { resultErr = r.catalog.classifyReadCancellation(caller, resultErr) }(ctx)
	}
	now := time.Now().UTC()
	if err := q.Validate(now); err != nil {
		return activity.ScriptActivity{}, err
	}
	s, err := r.begin(ctx)
	if err != nil {
		return activity.ScriptActivity{}, err
	}
	defer s.close()

	since := q.Since.UTC().UnixMilli()
	result := activity.ScriptActivity{Since: q.Since.UTC(), CoveredFrom: q.Since.UTC(), GeneratedAt: now, Scripts: []activity.ScriptTally{}}
	ids := []string{}
	args := []any{}
	for ordinal := range s.lease.sources {
		// The newest bounded slice of this source's requests, by sequence.
		var floor int64
		var reached bool
		err := s.tx.QueryRowContext(s.lease.ctx, fmt.Sprintf(`SELECT COALESCE(min(sequence),0),count(*)=? FROM (SELECT sequence FROM s%d.runtime_activities WHERE kind IN('exchange.started','exchange.completed') ORDER BY sequence DESC LIMIT ?)`, ordinal), activity.MaximumScriptActivityRequests, activity.MaximumScriptActivityRequests).Scan(&floor, &reached)
		if err != nil {
			return activity.ScriptActivity{}, err
		}
		if reached {
			var oldest int64
			if err := s.tx.QueryRowContext(s.lease.ctx, fmt.Sprintf(`SELECT occurred_at_unix_ms FROM s%d.runtime_activities WHERE sequence=?`, ordinal), floor).Scan(&oldest); err != nil {
				return activity.ScriptActivity{}, err
			}
			if oldest > since {
				result.Truncated = true
				if covered := time.UnixMilli(oldest).UTC(); covered.After(result.CoveredFrom) {
					result.CoveredFrom = covered
				}
			}
		}
		ids = append(ids, fmt.Sprintf(`SELECT subject_id FROM s%d.runtime_activities WHERE kind IN('exchange.started','exchange.completed') AND sequence>=? AND occurred_at_unix_ms>=?`, ordinal))
		args = append(args, floor, since)
	}
	cte := `WITH ids AS (` + strings.Join(ids, ` UNION `) + `)`
	cte, args, err = s.canonicalFromIDs(activity.PageRequest{}, `a.subject_id,a.kind,a.occurred_at_unix_ms,{{script_outcomes}}`, cte, args, false)
	if err != nil {
		return activity.ScriptActivity{}, err
	}
	rows, err := s.tx.QueryContext(s.lease.ctx, cte+` SELECT occurred_at_unix_ms,script_outcomes FROM canonical WHERE kind='exchange.completed' AND occurred_at_unix_ms>=?`, append(args, result.CoveredFrom.UnixMilli())...)
	if err != nil {
		return activity.ScriptActivity{}, err
	}
	tallies := map[string]*activity.ScriptTally{}
	for rows.Next() {
		var occurred int64
		var encoded []byte
		if err = rows.Scan(&occurred, &encoded); err != nil {
			break
		}
		if !validActivityMillis(occurred) {
			err = ErrInspectionUnavailable
			break
		}
		result.Requests++
		outcomes, decodeErr := decodeScriptOutcomes(encoded)
		if decodeErr != nil || activity.ValidateScriptOutcomes(outcomes) != nil {
			// One unreadable record is left out rather than hiding the rest.
			continue
		}
		activity.TallyScriptOutcomes(tallies, outcomes)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return activity.ScriptActivity{}, err
	}
	s.endSQL()
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return activity.ScriptActivity{}, err
	}
	for _, tally := range tallies {
		sort.Slice(tally.BlockReasons, func(left, right int) bool {
			a, b := tally.BlockReasons[left], tally.BlockReasons[right]
			return a.Count > b.Count || (a.Count == b.Count && a.Reason < b.Reason)
		})
		if len(tally.BlockReasons) > activity.MaximumScriptBlockReasons {
			tally.BlockReasons = tally.BlockReasons[:activity.MaximumScriptBlockReasons]
		}
		result.Scripts = append(result.Scripts, *tally)
	}
	sort.Slice(result.Scripts, func(left, right int) bool {
		return result.Scripts[left].ScriptID < result.Scripts[right].ScriptID
	})
	return result, nil
}
