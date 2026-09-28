package runtimepersistence

import (
	"context"
	"fmt"
	"sort"

	"github.com/vibe-agi/vibermate/internal/activity"
)

func (repository *activityRepository) SummarizeExchanges(ctx context.Context, scope activity.SummaryScope) (activity.ExchangeSummary, error) {
	if scope.Validate() != nil {
		return activity.ExchangeSummary{}, activity.ErrInvalidEvent
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return activity.ExchangeSummary{}, err
	}
	defer finish()
	candidates, args := exchangeCandidateQuery(activity.PageRequest{
		CaptureRunID: scope.CaptureRunID, ManualCaptureID: scope.ManualCaptureID,
	})
	if scope.SessionID != "" {
		// The existing (client_kind, session_id, exchange_id) index bounds the
		// session before lifecycle grouping. Do not scan local Agent logs here.
		candidates += ` AND subject_id IN (
			SELECT exchange_id FROM runtime_exchange_agent_identities
			WHERE client_kind=? AND session_id=?)`
		args = append(args, scope.Client, scope.SessionID)
	}
	rows, err := repository.reads.QueryContext(operation, `SELECT status,
		CASE WHEN status='failed' THEN reason_code ELSE '' END AS failure_code,
		count(*),min(occurred_at_unix_ms),max(occurred_at_unix_ms)
		FROM (`+candidates+`) GROUP BY status,failure_code`, args...)
	if err != nil {
		return activity.ExchangeSummary{}, err
	}
	defer rows.Close()
	result := activity.ExchangeSummary{Scope: scope, Failures: []activity.FailureCount{}}
	for rows.Next() {
		var status activity.Status
		var reason string
		var count int
		var first, last int64
		if err := rows.Scan(&status, &reason, &count, &first, &last); err != nil {
			return activity.ExchangeSummary{}, err
		}
		result.Requests += count
		switch status {
		case activity.StatusSucceeded:
			result.Succeeded += count
		case activity.StatusFailed:
			result.Failed += count
			result.Failures = append(result.Failures, activity.FailureCount{ReasonCode: reason, Count: count})
		case activity.StatusCanceled:
			result.Canceled += count
		case activity.StatusPending:
			result.Pending += count
		default:
			return activity.ExchangeSummary{}, fmt.Errorf("invalid exchange summary status: %s", status)
		}
		firstAt, lastAt := fromUnixMillis(first), fromUnixMillis(last)
		if result.FirstObservedAt == nil || firstAt.Before(*result.FirstObservedAt) {
			result.FirstObservedAt = &firstAt
		}
		if result.LastObservedAt == nil || lastAt.After(*result.LastObservedAt) {
			result.LastObservedAt = &lastAt
		}
	}
	if err := rows.Err(); err != nil {
		return activity.ExchangeSummary{}, err
	}
	sort.Slice(result.Failures, func(i, j int) bool {
		left, right := result.Failures[i], result.Failures[j]
		if left.Count != right.Count {
			return left.Count > right.Count
		}
		return left.ReasonCode < right.ReasonCode
	})
	if len(result.Failures) > 10 {
		for _, failure := range result.Failures[10:] {
			result.OtherFailures += failure.Count
		}
		result.Failures = result.Failures[:10]
	}
	return result, nil
}
