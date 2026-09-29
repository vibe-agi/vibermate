package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

// ScanUsage holds one read-only WAL snapshot for its version, policy, page keys
// and aggregates. It streams numeric buckets without an observation-count cap.
// The visitor must not re-enter the repository while this read is open.
func (store *Store) ScanUsage(ctx context.Context, query runtimeusage.AggregationQuery, userID runtimeuser.UserID, now time.Time, visit func(runtimeusage.UsageBucket) error) (runtimeusage.AggregationResult, error) {
	if err := query.Validate(); err != nil || now.IsZero() || visit == nil || (userID != "" && !userID.Valid()) {
		return runtimeusage.AggregationResult{}, runtimeusage.ErrInvalidQuery
	}
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return runtimeusage.AggregationResult{}, err
	}
	defer finish()
	tx, err := store.reads.BeginTx(operation, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return runtimeusage.AggregationResult{}, err
	}
	defer tx.Rollback()
	result, err := usageReadSnapshot(operation, tx, query, userID, now)
	if err != nil {
		return runtimeusage.AggregationResult{}, err
	}
	if query.Snapshot != "" && query.Snapshot != result.Snapshot {
		return runtimeusage.AggregationResult{}, runtimeusage.ErrSnapshotChanged
	}
	if query.KnownSnapshot == result.Snapshot {
		result.Unchanged = true
		return result, tx.Commit()
	}
	where, args := usageWhere(query.Period, userID, now)
	for _, filter := range query.Filters {
		where += " AND " + usageColumn(filter.Dimension) + "=?"
		args = append(args, filter.ID)
	}
	if query.Dimension == "" {
		err = scanUsageDays(operation, tx, query.Period, where, args, visit)
	} else {
		result.Groups, result.NextCursor, err = scanUsageGroups(operation, tx, query, result.Snapshot, where, args, visit)
	}
	if err != nil {
		return runtimeusage.AggregationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return runtimeusage.AggregationResult{}, err
	}
	return result, nil
}

func usageWhere(query runtimeusage.Query, userID runtimeuser.UserID, now time.Time) (string, []any) {
	from, until := query.Bounds()
	where := `occurred_at_unix_ms>=? AND occurred_at_unix_ms<? AND expires_at_unix_ms>?
 AND NOT EXISTS (SELECT 1 FROM runtime_usage_retention_caps
 WHERE sequence>after_sequence AND sequence<=through_sequence AND occurred_at_unix_ms<=?-retention_days*86400000)`
	args := []any{from.UnixMilli(), until.UnixMilli(), now.UnixMilli(), now.UnixMilli()}
	if userID != "" {
		where += " AND runtime_user_id=?"
		args = append(args, userID)
	}
	return where, args
}

// Only constant, allowlisted column names enter SQL. All values use bindings.
func usageColumn(dimension string) string {
	switch dimension {
	case "source", "client":
		return dimension
	case "profile":
		return "environment_id"
	case "account", "model", "caller", "project", "branch", "session":
		return dimension + "_id"
	case "capture":
		return "capture_run_id"
	case "manualCapture":
		return "manual_capture_id"
	}
	panic("usage dimension was not validated")
}

func usageReadSnapshot(ctx context.Context, tx *sql.Tx, query runtimeusage.AggregationQuery, userID runtimeuser.UserID, now time.Time) (runtimeusage.AggregationResult, error) {
	policy, err := scanUsagePolicy(tx.QueryRowContext(ctx, `SELECT enabled,retention_days,revision,collecting_since_unix_ms FROM runtime_usage_policy WHERE singleton=1`))
	if err != nil {
		return runtimeusage.AggregationResult{}, err
	}
	where, args := usageWhere(query.Period, userID, now)
	var count, latest int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(max(sequence),0) FROM runtime_usage_observations WHERE `+where, args...).Scan(&count, &latest); err != nil {
		return runtimeusage.AggregationResult{}, err
	}
	// Hash rather than expose a global insertion sequence to a member. A change
	// outside that member's period/scope cannot alter their snapshot. Immutable
	// rows + non-reused sequence distinguish insert/delete even at equal counts.
	data, err := json.Marshal(struct {
		Period          runtimeusage.Period
		User            runtimeuser.UserID
		Policy          runtimeusage.CollectionPolicy
		Count, Latest   int64
		PricingRevision string
	}{query.Period.Period(), userID, policy, count, latest, query.PricingRevision})
	if err != nil {
		return runtimeusage.AggregationResult{}, err
	}
	digest := sha256.Sum256(data)
	return runtimeusage.AggregationResult{Collection: policy, Snapshot: hex.EncodeToString(digest[:]), Groups: []runtimeusage.GroupUsage{}}, nil
}

const usageBucketColumns = `model_id,status,input_tokens,cache_read_tokens,cache_write_tokens,output_tokens,reasoning_tokens`

func scanUsageDays(ctx context.Context, tx *sql.Tx, period runtimeusage.Query, where string, args []any, visit func(runtimeusage.UsageBucket) error) error {
	windows := period.DayWindows()
	values := make([]string, len(windows))
	boundaries := make([]any, 0, 3*len(windows)+len(args))
	for index, day := range windows {
		values[index] = "(?,?,?)"
		boundaries = append(boundaries, day.Date, day.From.UnixMilli(), day.Until.UnixMilli())
	}
	statement := `WITH days(date,starts,ends) AS (VALUES ` + strings.Join(values, ",") + `)
 SELECT days.date,` + usageBucketColumns + `,count(*)
 FROM days JOIN runtime_usage_observations ON occurred_at_unix_ms>=days.starts AND occurred_at_unix_ms<days.ends
 WHERE ` + where + ` GROUP BY days.date,` + usageBucketColumns + ` ORDER BY days.date`
	return scanUsageBuckets(ctx, tx, statement, append(boundaries, args...), true, visit)
}

type usageCursor struct {
	Snapshot string `json:"snapshot"`
	Query    string `json:"query"`
	Calls    int    `json:"calls"`
	ID       string `json:"id"`
}

func usageGroupKey(query runtimeusage.AggregationQuery) string {
	filters := append([]runtimeusage.Filter(nil), query.Filters...)
	sort.Slice(filters, func(i, j int) bool { return filters[i].Dimension < filters[j].Dimension })
	data, _ := json.Marshal(struct {
		Dimension string
		Filters   []runtimeusage.Filter
	}{query.Dimension, filters})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func scanUsageGroups(ctx context.Context, tx *sql.Tx, query runtimeusage.AggregationQuery, snapshot, where string, args []any, visit func(runtimeusage.UsageBucket) error) ([]runtimeusage.GroupUsage, string, error) {
	column := usageColumn(query.Dimension)
	statement := `SELECT ` + column + `,count(*) AS calls,max(sequence) FROM runtime_usage_observations WHERE ` + where + ` GROUP BY ` + column
	pageArgs := append([]any(nil), args...)
	key := usageGroupKey(query)
	if query.Cursor != "" {
		var cursor usageCursor
		data, err := base64.RawURLEncoding.DecodeString(query.Cursor)
		if err != nil {
			return nil, "", runtimeusage.ErrInvalidQuery
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		var extra any
		if err := decoder.Decode(&cursor); err != nil || !errors.Is(decoder.Decode(&extra), io.EOF) || cursor.Calls < 1 || len(cursor.ID) > 1024 || cursor.Query != key {
			return nil, "", runtimeusage.ErrInvalidQuery
		}
		if cursor.Snapshot != snapshot {
			return nil, "", runtimeusage.ErrSnapshotChanged
		}
		statement += ` HAVING calls<? OR (calls=? AND ` + column + `>?)`
		pageArgs = append(pageArgs, cursor.Calls, cursor.Calls, cursor.ID)
	}
	statement += ` ORDER BY calls DESC,` + column + ` LIMIT ?`
	pageArgs = append(pageArgs, query.Limit+1)
	rows, err := tx.QueryContext(ctx, statement, pageArgs...)
	if err != nil {
		return nil, "", err
	}
	groups := make([]runtimeusage.GroupUsage, 0, query.Limit)
	sequences := make([]int64, 0, query.Limit)
	more := false
	for rows.Next() {
		if len(groups) == query.Limit {
			more = true
			break
		}
		group := runtimeusage.GroupUsage{Dimension: query.Dimension}
		var sequence int64
		if err := rows.Scan(&group.ID, &group.AgentAPICalls, &sequence); err != nil {
			rows.Close()
			return nil, "", err
		}
		groups = append(groups, group)
		sequences = append(sequences, sequence)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil || len(groups) == 0 {
		return groups, "", err
	}
	// Only page labels touch JSON, at most MaxGroupPage rows, with the newest
	// frozen label for each ID. We never relabel history from mutable accounts.
	for index := range groups {
		if err := usageGroupLabel(ctx, tx, sequences[index], &groups[index]); err != nil {
			return nil, "", err
		}
	}
	placeholders := make([]string, len(groups))
	bucketArgs := append([]any(nil), args...)
	for index, group := range groups {
		placeholders[index] = "?"
		bucketArgs = append(bucketArgs, group.ID)
	}
	statement = `SELECT ` + column + `,` + usageBucketColumns + `,count(*) FROM runtime_usage_observations WHERE ` + where +
		` AND ` + column + ` IN (` + strings.Join(placeholders, ",") + `) GROUP BY ` + column + `,` + usageBucketColumns
	if err := scanUsageBuckets(ctx, tx, statement, bucketArgs, false, visit); err != nil {
		return nil, "", err
	}
	var next string
	if more {
		last := groups[len(groups)-1]
		data, err := json.Marshal(usageCursor{Snapshot: snapshot, Query: key, Calls: last.AgentAPICalls, ID: last.ID})
		if err != nil {
			return nil, "", err
		}
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	return groups, next, nil
}

func usageGroupLabel(ctx context.Context, tx *sql.Tx, sequence int64, group *runtimeusage.GroupUsage) error {
	label, evidence := usageColumn(group.Dimension), "''"
	switch group.Dimension {
	case "profile":
		label = "coalesce(json_extract(observation_json,'$.environmentName'),'')"
	case "account":
		label = "coalesce(json_extract(observation_json,'$.accountName'),'')"
	case "caller":
		label = "coalesce(json_extract(observation_json,'$.attribution.callerLabel'),'')"
		evidence = "coalesce(json_extract(observation_json,'$.attribution.callerKind'),'')"
	case "project":
		label = "coalesce(json_extract(observation_json,'$.attribution.gitAtLaunch.repositoryName'),'')"
		evidence = "coalesce(json_extract(observation_json,'$.attribution.gitAtLaunch.repositorySource'),'')"
	case "branch":
		label = "coalesce(json_extract(observation_json,'$.attribution.gitAtLaunch.branch'),'')"
		evidence = "CASE WHEN branch_id='detached' THEN 'detached' WHEN branch_id<>'' THEN 'launch_snapshot' ELSE '' END"
	}
	return tx.QueryRowContext(ctx, `SELECT `+label+`,`+evidence+` FROM runtime_usage_observations WHERE sequence=?`, sequence).Scan(&group.Label, &group.Evidence)
}

func scanUsageBuckets(ctx context.Context, tx *sql.Tx, statement string, args []any, daily bool, visit func(runtimeusage.UsageBucket) error) error {
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket runtimeusage.UsageBucket
		var key string
		var input, cacheRead, cacheWrite, output, reasoning sql.NullInt64
		if err := rows.Scan(&key, &bucket.Model, &bucket.Status, &input, &cacheRead, &cacheWrite, &output, &reasoning, &bucket.Calls); err != nil {
			return err
		}
		if bucket.Calls < 1 {
			return fmt.Errorf("invalid usage bucket count: %d", bucket.Calls)
		}
		if daily {
			bucket.Day = key
		} else {
			bucket.GroupID = key
		}
		value := func(number sql.NullInt64) protocolcore.UsageValue {
			return protocolcore.UsageValue{Known: number.Valid, Tokens: number.Int64}
		}
		bucket.Usage = protocolcore.Usage{
			InputUncached: value(input), CacheRead: value(cacheRead), CacheWrite: value(cacheWrite),
			Output: value(output), Reasoning: value(reasoning),
		}
		if err := visit(bucket); err != nil {
			return err
		}
	}
	return rows.Err()
}
