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
	"reflect"
	"strings"
	"time"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

// UsageInspectionReader aggregates immutable Exchange observations across the
// declared source set. SQL ranks globally; local sequences never cross sources.
type UsageInspectionReader struct {
	catalog    *InspectionCatalog
	visibility recording.InspectionVisibility
}

func NewUsageInspectionReader(c *InspectionCatalog, v recording.InspectionVisibility) *UsageInspectionReader {
	return &UsageInspectionReader{catalog: c, visibility: v}
}
func (r *UsageInspectionReader) Begin(ctx context.Context) (recording.InspectionToken, error) {
	return NewInspectionBoundary(r.visibility, r.catalog).Begin(ctx)
}
func (r *UsageInspectionReader) Check(ctx context.Context, t recording.InspectionToken) error {
	return NewInspectionBoundary(r.visibility, r.catalog).Check(ctx, t)
}

type usageInspectionRead struct {
	lease   *inspectionLease
	tx      *sql.Tx
	token   recording.InspectionToken
	mirror  inspectionMirror
	current string
	cancel  context.CancelFunc
	cte     string
	args    []any
}

func (s *usageInspectionRead) endSQL() {
	if s.tx != nil {
		s.tx.Rollback()
		s.tx = nil
	}
}
func (s *usageInspectionRead) close() { s.endSQL(); s.lease.close(); s.cancel() }
func (r *UsageInspectionReader) begin(ctx context.Context) (*usageInspectionRead, error) {
	if ctx == nil || r == nil || r.catalog == nil || r.visibility == nil {
		return nil, ErrInspectionUnavailable
	}
	work, cancel := context.WithTimeout(ctx, r.catalog.timeout)
	snapshot, err := r.visibility.ReadInspectionSnapshot(work, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	if !snapshot.Stamp.Valid() || snapshot.Stamp.LayoutID != r.catalog.layout {
		cancel()
		return nil, ErrInspectionUnavailable
	}
	lease, err := r.catalog.acquire(work)
	if err != nil {
		cancel()
		return nil, err
	}
	s := &usageInspectionRead{lease: lease, token: recording.InspectionToken{Control: snapshot.Stamp, Sources: lease.version}, current: fmt.Sprintf("s%d", len(lease.sources)-1), cancel: cancel}
	s.tx, err = lease.conn.BeginTx(lease.ctx, &sql.TxOptions{ReadOnly: true})
	if err == nil {
		s.mirror, err = readInspectionMirror(lease.ctx, s.tx, s.current, snapshot.Stamp)
	}
	if err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func usageInspectionScope(current bool, prefix string) string {
	if current {
		return prefix + `recording_scope_kind,` + prefix + `recording_scope_id,` + prefix + `recording_archive_epoch,` + prefix + `recording_capture_generation`
	}
	return `CASE WHEN ` + prefix + `capture_run_id<>'' THEN 'managed_run' WHEN ` + prefix + `manual_capture_id<>'' THEN 'manual_capture' ELSE '' END AS recording_scope_kind,CASE WHEN ` + prefix + `capture_run_id<>'' THEN ` + prefix + `capture_run_id ELSE ` + prefix + `manual_capture_id END AS recording_scope_id,1 AS recording_archive_epoch,CASE WHEN ` + prefix + `capture_run_id<>'' OR ` + prefix + `manual_capture_id<>'' THEN 1 ELSE 0 END AS recording_capture_generation`
}

// Only candidate identities are unioned. Every copy of those identities is
// considered before owner, visibility, retention and dimension filters.
func (s *usageInspectionRead) prepare(q runtimeusage.AggregationQuery, user runtimeuser.UserID, now time.Time) error {
	from, until := q.Period.Bounds()
	ids := []string{}
	for i := range s.lease.sources {
		statement := fmt.Sprintf(`SELECT exchange_id FROM s%d.runtime_usage_observations WHERE occurred_at_unix_ms>=? AND occurred_at_unix_ms<?`, i)
		s.args = append(s.args, from.UnixMilli(), until.UnixMilli())
		if user != "" {
			statement += ` AND runtime_user_id=?`
			s.args = append(s.args, user)
		}
		ids = append(ids, statement)
	}
	s.cte = `WITH ids AS (` + strings.Join(ids, ` UNION `) + `)`
	if err := s.compareCopies(); err != nil {
		return err
	}
	copies := []string{}
	columns := `sequence,exchange_id,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,expires_at_unix_ms,status,source,environment_id,account_id,model_id,caller_id,project_id,branch_id,client,session_id,input_tokens,cache_read_tokens,cache_write_tokens,output_tokens,reasoning_tokens`
	for i, source := range s.lease.sources {
		revision := "0"
		if source.Current {
			revision = "policy_revision"
		}
		copies = append(copies, fmt.Sprintf(`SELECT %d AS ordinal,%s,%s AS policy_revision,%s FROM s%d.runtime_usage_observations WHERE exchange_id IN(SELECT exchange_id FROM ids)`, i, columns, revision, usageInspectionScope(source.Current, ""), i))
	}
	s.cte += `, copies AS (` + strings.Join(copies, ` UNION ALL `) + `)`
	// Avoid overflow when capping a malformed timestamp: the delta is bounded
	// and only candidate-period copies reach output after consistency checking.
	effective := `min(expires_at_unix_ms,coalesce((SELECT min(CASE WHEN occurred_at_unix_ms>9223372036854775807-retention_days*86400000 THEN 9223372036854775807 ELSE occurred_at_unix_ms+retention_days*86400000 END) FROM ` + s.current + `.inspection_mirror_usage_caps WHERE mirror_generation=? AND policy_revision<=through_policy_revision),9223372036854775807)`
	s.args = append(s.args, s.mirror.Generation)
	for i, source := range s.lease.sources {
		if source.Current {
			continue
		}
		effective += `,coalesce((SELECT min(CASE WHEN occurred_at_unix_ms>9223372036854775807-retention_days*86400000 THEN 9223372036854775807 ELSE occurred_at_unix_ms+retention_days*86400000 END) FROM ` + s.current + `.inspection_mirror_legacy_caps WHERE mirror_generation=? AND archive_id=? AND ordinal=? AND sequence>after_sequence AND sequence<=through_sequence),9223372036854775807)`
		s.args = append(s.args, s.mirror.Generation, source.ID, i)
	}
	effective += `)`
	s.cte += `, capped AS (SELECT *,` + effective + ` AS effective_expiry FROM copies), winners AS (SELECT exchange_id,max(ordinal) AS ordinal,min(effective_expiry) AS logical_expiry FROM capped GROUP BY exchange_id), visible AS (SELECT c.*,w.logical_expiry FROM capped c JOIN winners w ON w.exchange_id=c.exchange_id AND w.ordinal=c.ordinal WHERE NOT EXISTS(SELECT 1 FROM ` + s.current + `.recording_denials d WHERE d.kind='usage' AND d.identity=c.exchange_id) AND c.occurred_at_unix_ms>=? AND c.occurred_at_unix_ms<? AND w.logical_expiry>? AND c.recording_archive_epoch=? AND ((c.recording_scope_kind='' AND c.recording_scope_id='' AND c.recording_capture_generation=0 AND c.capture_run_id='' AND c.manual_capture_id='') OR (((c.recording_scope_kind='managed_run' AND c.recording_scope_id=c.capture_run_id AND c.manual_capture_id='') OR (c.recording_scope_kind='manual_capture' AND c.recording_scope_id=c.manual_capture_id AND c.capture_run_id='')) AND EXISTS(SELECT 1 FROM ` + s.current + `.inspection_mirror_fences f WHERE f.mirror_generation=? AND f.capture_kind=c.recording_scope_kind AND f.capture_id=c.recording_scope_id AND f.capture_generation=c.recording_capture_generation AND f.deleted=0)))`
	s.args = append(s.args, from.UnixMilli(), until.UnixMilli(), now.UnixMilli(), s.token.Control.ArchiveEpoch, s.mirror.Generation)
	if user != "" {
		s.cte += ` AND c.runtime_user_id=?`
		s.args = append(s.args, user)
	}
	s.cte += `)`
	return nil
}

// Pairwise indexed reads stream only overlapping observations. Decoding bounded
// JSON compares facts, not JSON key order or timestamp formatting. Stored expiry
// and policy coordinates intentionally differ across legitimate retained copies.
func (s *usageInspectionRead) compareCopies() error {
	for i := range s.lease.sources {
		for j := i + 1; j < len(s.lease.sources); j++ {
			statement := s.cte + fmt.Sprintf(` SELECT CASE WHEN length(CAST(a.observation_json AS BLOB))<=65536 THEN a.observation_json END,CASE WHEN length(CAST(b.observation_json AS BLOB))<=65536 THEN b.observation_json END,a.exchange_id,a.capture_run_id,a.manual_capture_id,a.runtime_user_id,a.occurred_at_unix_ms,b.exchange_id,b.capture_run_id,b.manual_capture_id,b.runtime_user_id,b.occurred_at_unix_ms,%s,%s FROM s%d.runtime_usage_observations a JOIN s%d.runtime_usage_observations b ON a.exchange_id=b.exchange_id WHERE a.exchange_id IN(SELECT exchange_id FROM ids)`, usageInspectionScope(s.lease.sources[i].Current, "a."), usageInspectionScope(s.lease.sources[j].Current, "b."), i, j)
			rows, err := s.tx.QueryContext(s.lease.ctx, statement, s.args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var rawA, rawB string
				type identity struct {
					id, capture, manual, user string
					at                        int64
					kind, scope               string
					epoch, generation         int64
				}
				var a, b identity
				if err = rows.Scan(&rawA, &rawB, &a.id, &a.capture, &a.manual, &a.user, &a.at, &b.id, &b.capture, &b.manual, &b.user, &b.at, &a.kind, &a.scope, &a.epoch, &a.generation, &b.kind, &b.scope, &b.epoch, &b.generation); err != nil {
					break
				}
				var av, bv runtimeusage.Observation
				decode := func(raw string, v *runtimeusage.Observation, id identity) bool {
					if json.Unmarshal([]byte(raw), v) != nil || v.Validate() != nil || string(v.UserID) != id.user || v.ExchangeID != id.id || v.CaptureRunID != id.capture || v.ManualCaptureID != id.manual || v.OccurredAt.UnixMilli() != id.at {
						return false
					}
					v.StartedAt = v.StartedAt.UTC()
					v.OccurredAt = v.OccurredAt.UTC()
					return true
				}
				if a != b || !decode(rawA, &av, a) || !decode(rawB, &bv, b) || !reflect.DeepEqual(av, bv) {
					err = fmt.Errorf("%w: conflicting Usage Exchange identity", ErrInspectionUnavailable)
					break
				}
			}
			err = errors.Join(err, rows.Err(), rows.Close())
			if err != nil {
				return err
			}
		}
	}
	return nil
}

const usageInspectionMaximumBuckets = 65536
const usageInspectionMaximumBucketBytes = 8 << 20

func (r *UsageInspectionReader) ScanUsage(ctx context.Context, q runtimeusage.AggregationQuery, user runtimeuser.UserID, now time.Time, visit func(runtimeusage.UsageBucket) error) (resultValue runtimeusage.AggregationResult, resultErr error) {
	if r != nil {
		defer func(caller context.Context) { resultErr = r.catalog.classifyReadCancellation(caller, resultErr) }(ctx)
	}
	empty := runtimeusage.AggregationResult{}
	if q.Validate() != nil || now.IsZero() || visit == nil || (user != "" && !user.Valid()) {
		return empty, runtimeusage.ErrInvalidQuery
	}
	s, err := r.begin(ctx)
	if err != nil {
		return empty, err
	}
	defer s.close()
	if err = s.prepare(q, user, now); err != nil {
		return empty, err
	}
	result, err := s.snapshot(q, user)
	if err != nil {
		return empty, err
	}
	if q.Snapshot != "" && q.Snapshot != result.Snapshot {
		return empty, runtimeusage.ErrSnapshotChanged
	}
	buckets := []runtimeusage.UsageBucket{}
	size := 0
	collect := func(b runtimeusage.UsageBucket) error {
		if len(b.GroupID) > 1024 || len(b.Model) > 1024 || (b.Status != activity.StatusSucceeded && b.Status != activity.StatusFailed && b.Status != activity.StatusCanceled) {
			return ErrInspectionUnavailable
		}
		for _, v := range []protocolcore.UsageValue{b.Usage.InputUncached, b.Usage.CacheRead, b.Usage.CacheWrite, b.Usage.Output, b.Usage.Reasoning} {
			if v.Tokens < 0 {
				return ErrInspectionUnavailable
			}
		}
		if b.Usage.Output.Known && b.Usage.Reasoning.Known && b.Usage.Reasoning.Tokens > b.Usage.Output.Tokens {
			return ErrInspectionUnavailable
		}
		size += len(b.Day) + len(b.GroupID) + len(b.Model) + len(b.Status) + int(unsafe.Sizeof(runtimeusage.UsageBucket{}))
		if len(buckets) >= usageInspectionMaximumBuckets || size > usageInspectionMaximumBucketBytes {
			return ErrInspectionSourceLimit
		}
		buckets = append(buckets, b)
		return nil
	}
	if q.KnownSnapshot == result.Snapshot {
		result.Unchanged = true
	} else if q.Dimension == "" {
		err = s.days(q, collect)
	} else {
		result.Groups, result.NextCursor, err = s.groups(q, result.Snapshot, collect)
	}
	if err != nil {
		return empty, err
	}
	s.endSQL()
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return empty, err
	}
	for _, b := range buckets {
		if err = s.lease.ctx.Err(); err != nil {
			return empty, err
		}
		if err = visit(b); err != nil {
			return empty, err
		}
	}
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return empty, err
	}
	return result, nil
}
func (s *usageInspectionRead) snapshot(q runtimeusage.AggregationQuery, user runtimeuser.UserID) (runtimeusage.AggregationResult, error) {
	type stat struct {
		Ordinal               int
		Count, Latest, Expiry int64
	}
	stats := []stat{}
	rows, err := s.tx.QueryContext(s.lease.ctx, s.cte+` SELECT ordinal,count(*),max(sequence),min(logical_expiry) FROM visible GROUP BY ordinal ORDER BY ordinal`, s.args...)
	if err != nil {
		return runtimeusage.AggregationResult{}, err
	}
	for rows.Next() {
		var v stat
		if err = rows.Scan(&v.Ordinal, &v.Count, &v.Latest, &v.Expiry); err != nil {
			break
		}
		stats = append(stats, v)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return runtimeusage.AggregationResult{}, err
	}
	data, err := json.Marshal(struct {
		Token   recording.InspectionToken
		Period  runtimeusage.Period
		User    runtimeuser.UserID
		Policy  runtimeusage.CollectionPolicy
		Stats   []stat
		Pricing string
	}{s.token, q.Period.Period(), user, s.mirror.Policy, stats, q.PricingRevision})
	if err != nil {
		return runtimeusage.AggregationResult{}, err
	}
	digest := sha256.Sum256(data)
	var expiry time.Time
	for _, v := range stats {
		at := time.UnixMilli(v.Expiry).UTC()
		if expiry.IsZero() || at.Before(expiry) {
			expiry = at
		}
	}
	return runtimeusage.AggregationResult{ExpiresAt: expiry, Collection: s.mirror.Policy, Snapshot: hex.EncodeToString(digest[:]), Groups: []runtimeusage.GroupUsage{}}, nil
}
func (s *usageInspectionRead) filtered(q runtimeusage.AggregationQuery) (string, []any) {
	where := "1=1"
	args := append([]any(nil), s.args...)
	for _, f := range q.Filters {
		where += ` AND ` + usageColumn(f.Dimension) + `=?`
		args = append(args, f.ID)
	}
	return where, args
}
func (s *usageInspectionRead) days(q runtimeusage.AggregationQuery, visit func(runtimeusage.UsageBucket) error) error {
	where, args := s.filtered(q)
	windows := q.Period.DayWindows()
	values := make([]string, len(windows))
	boundaries := []any{}
	for i, w := range windows {
		values[i] = "(?,?,?)"
		boundaries = append(boundaries, w.Date, w.From.UnixMilli(), w.Until.UnixMilli())
	}
	// CTE bindings precede civil-day windows and then dimension filters.
	all := append(append(append([]any(nil), s.args...), boundaries...), args[len(s.args):]...)
	statement := s.cte + `, days(date,starts,ends) AS (VALUES ` + strings.Join(values, ",") + `) SELECT days.date,` + usageBucketColumns + `,count(*) FROM days JOIN visible ON occurred_at_unix_ms>=days.starts AND occurred_at_unix_ms<days.ends WHERE ` + where + ` GROUP BY days.date,` + usageBucketColumns + ` ORDER BY days.date`
	return scanUsageBuckets(s.lease.ctx, s.tx, statement, all, true, visit)
}
func (s *usageInspectionRead) groups(q runtimeusage.AggregationQuery, snapshot string, visit func(runtimeusage.UsageBucket) error) ([]runtimeusage.GroupUsage, string, error) {
	where, args := s.filtered(q)
	column := usageColumn(q.Dimension)
	key := usageGroupKey(q)
	// Choose each group's source/local label coordinate in the same global
	// ranking query, rather than rescan the complete projection per page label.
	statement := s.cte + `, group_members AS (SELECT ` + column + `,ordinal,sequence,row_number() OVER(PARTITION BY ` + column + ` ORDER BY ordinal DESC,sequence DESC) AS label_rank FROM visible WHERE ` + where + `) SELECT ` + column + `,count(*) AS calls,max(CASE WHEN label_rank=1 THEN ordinal END),max(CASE WHEN label_rank=1 THEN sequence END) FROM group_members GROUP BY ` + column
	pageArgs := append([]any(nil), args...)
	if q.Cursor != "" {
		var cursor usageCursor
		data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil {
			return nil, "", runtimeusage.ErrInvalidQuery
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		var extra any
		if decoder.Decode(&cursor) != nil || !errors.Is(decoder.Decode(&extra), io.EOF) || cursor.Calls < 1 || len(cursor.ID) > 1024 || cursor.Query != key {
			return nil, "", runtimeusage.ErrInvalidQuery
		}
		if cursor.Snapshot != snapshot {
			return nil, "", runtimeusage.ErrSnapshotChanged
		}
		statement += ` HAVING calls<? OR (calls=? AND ` + column + `>?)`
		pageArgs = append(pageArgs, cursor.Calls, cursor.Calls, cursor.ID)
	}
	statement += ` ORDER BY calls DESC,` + column + ` LIMIT ?`
	pageArgs = append(pageArgs, q.Limit+1)
	rows, err := s.tx.QueryContext(s.lease.ctx, statement, pageArgs...)
	if err != nil {
		return nil, "", err
	}
	groups := []runtimeusage.GroupUsage{}
	type labelReference struct {
		ordinal  int
		sequence int64
	}
	labels := []labelReference{}
	more := false
	for rows.Next() {
		if len(groups) == q.Limit {
			more = true
			break
		}
		g := runtimeusage.GroupUsage{Dimension: q.Dimension}
		var ref labelReference
		if err = rows.Scan(&g.ID, &g.AgentAPICalls, &ref.ordinal, &ref.sequence); err != nil {
			break
		}
		groups = append(groups, g)
		labels = append(labels, ref)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, "", err
	}
	for i := range groups {
		ordinal, sequence := labels[i].ordinal, labels[i].sequence
		if ordinal < 0 || ordinal >= len(s.lease.sources) {
			return nil, "", ErrInspectionUnavailable
		}
		label, evidence := usageGroupLabelExpressions(q.Dimension)
		err = s.tx.QueryRowContext(s.lease.ctx, fmt.Sprintf(`SELECT %s,%s FROM s%d.runtime_usage_observations WHERE sequence=?`, label, evidence, ordinal), sequence).Scan(&groups[i].Label, &groups[i].Evidence)
		if err != nil {
			return nil, "", err
		}
		if len(groups[i].Label) > 1024 || len(groups[i].Evidence) > 1024 {
			return nil, "", ErrInspectionUnavailable
		}
	}
	if len(groups) == 0 {
		return groups, "", nil
	}
	placeholders := make([]string, len(groups))
	bucketArgs := append([]any(nil), args...)
	for i, g := range groups {
		placeholders[i] = "?"
		bucketArgs = append(bucketArgs, g.ID)
	}
	err = scanUsageBuckets(s.lease.ctx, s.tx, s.cte+` SELECT `+column+`,`+usageBucketColumns+`,count(*) FROM visible WHERE `+where+` AND `+column+` IN (`+strings.Join(placeholders, ",")+`) GROUP BY `+column+`,`+usageBucketColumns, bucketArgs, false, visit)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if more {
		g := groups[len(groups)-1]
		data, err := json.Marshal(usageCursor{Snapshot: snapshot, Query: key, Calls: g.AgentAPICalls, ID: g.ID})
		if err != nil {
			return nil, "", err
		}
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	return groups, next, nil
}

var _ runtimeusage.InspectionReader = (*UsageInspectionReader)(nil)
