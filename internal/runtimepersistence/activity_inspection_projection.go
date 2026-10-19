package runtimepersistence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func activityScopeWhere(q activity.PageRequest, prefix string) (string, []any) {
	where := []string{prefix + `kind IN('exchange.started','exchange.completed')`}
	args := []any{}
	for _, f := range []struct{ column, value string }{{"capture_run_id", q.CaptureRunID}, {"manual_capture_id", q.ManualCaptureID}, {"environment_id", q.EnvironmentID}} {
		if f.value != "" {
			where = append(where, prefix+f.column+"<>'' AND "+prefix+f.column+"=?")
			args = append(args, f.value)
		}
	}
	return strings.Join(where, " AND "), args
}
func activityScopeSQL(source InspectionSource, prefix string) (epoch, generation, kind, id string) {
	kind = `CASE WHEN ` + prefix + `capture_run_id<>'' THEN 'managed_run' WHEN ` + prefix + `manual_capture_id<>'' THEN 'manual_capture' ELSE '' END`
	id = `CASE WHEN ` + prefix + `capture_run_id<>'' THEN ` + prefix + `capture_run_id ELSE ` + prefix + `manual_capture_id END`
	if source.Current {
		return prefix + "recording_archive_epoch", prefix + "recording_capture_generation", kind, id
	}
	return "1", `CASE WHEN ` + prefix + `capture_run_id<>'' OR ` + prefix + `manual_capture_id<>'' THEN 1 ELSE 0 END`, kind, id
}
func (s *activityInspectionRead) visibleSQL(source InspectionSource, prefix string) string {
	epoch, generation, kind, id := activityScopeSQL(source, prefix)
	return fmt.Sprintf(`(%s=%d AND ((%s='' AND %s='' AND %s=0) OR EXISTS(SELECT 1 FROM s%d.inspection_mirror_fences f WHERE f.mirror_generation=%d AND f.capture_kind=(%s) AND f.capture_id=(%s) AND f.capture_generation=(%s) AND f.deleted=0)))`, epoch, s.token.Control.ArchiveEpoch, prefix+"capture_run_id", prefix+"manual_capture_id", generation, len(s.lease.sources)-1, s.mirror.Generation, kind, id, generation)
}
func activityScopeOf(record activity.Record, fence recording.Fence) recording.Scope {
	scope := recording.Scope{Fence: fence}
	if record.CaptureRunID != "" {
		scope.Capture = captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: record.CaptureRunID}
	} else if record.ManualCaptureID != "" {
		scope.Capture = captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: record.ManualCaptureID}
	}
	return scope
}

// The relation retains Exchange identity until global lifecycle selection and
// visibility. Only narrow columns enter aggregation; body/transport payloads
// never ride through GROUP BY. Overlapping copies are compared as bounded
// typed rows, streamed without an all-history Go map.
func (s *activityInspectionRead) canonicalRelation(q activity.PageRequest, columns string) (string, []any, error) {
	ids := []string{}
	args := []any{}
	for ordinal := range s.lease.sources {
		where, values := activityScopeWhere(q, "")
		ids = append(ids, fmt.Sprintf(`SELECT subject_id FROM s%d.runtime_activities WHERE `, ordinal)+where)
		args = append(args, values...)
	}
	cte := `WITH ids AS (` + strings.Join(ids, ` UNION `) + `)`
	return s.canonicalFromIDs(q, columns, cte, args, false)
}
func (s *activityInspectionRead) canonicalFromIDs(q activity.PageRequest, columns, cte string, args []any, scopedIDs bool) (string, []any, error) {
	if err := s.compareLifecycleCopies(cte, args); err != nil {
		return "", nil, err
	}
	if scopedIDs {
		mismatches := []string{}
		for ordinal, source := range s.lease.sources {
			mismatches = append(mismatches, fmt.Sprintf(`SELECT 1 FROM s%d.runtime_activities a WHERE a.kind IN('exchange.started','exchange.completed') AND a.subject_id IN(SELECT subject_id FROM ids) AND `, ordinal)+activityBetterExchange(s, ordinal)+` AND `+s.visibleSQL(source, "a.")+` AND NOT(`+activityIdentityScopeMatch(source)+`)`)
		}
		var mismatch bool
		if err := s.tx.QueryRowContext(s.lease.ctx, cte+` SELECT EXISTS(SELECT 1 FROM (`+strings.Join(mismatches, ` UNION ALL `)+`))`, args...).Scan(&mismatch); err != nil {
			return "", nil, err
		}
		if mismatch {
			return "", nil, activity.ErrInvalidEvent
		}
	}
	copies := []string{}
	for ordinal, source := range s.lease.sources {
		where, values := activityScopeWhere(q, "a.")
		args = append(args, values...)
		statement := fmt.Sprintf(`SELECT %d AS ordinal,a.sequence,%s FROM s%d.runtime_activities a WHERE `, ordinal, activityProjectionColumns(ordinal, source, columns), ordinal) + where + ` AND a.subject_id IN(SELECT subject_id FROM ids) AND ` + activityBetterExchange(s, ordinal) + ` AND ` + s.visibleSQL(source, "a.")
		if scopedIDs {
			statement += ` AND ` + activityIdentityScopeMatch(source)
		}
		copies = append(copies, statement)
	}
	return cte + `, canonical AS (` + strings.Join(copies, ` UNION ALL `) + `)`, args, nil
}
func (s *activityInspectionRead) compareLifecycleCopies(cte string, args []any) error {
	for i := range s.lease.sources {
		for j := i; j < len(s.lease.sources); j++ {
			predicate := ""
			if i == j {
				predicate = " AND a.sequence<b.sequence"
			}
			columnsB := strings.ReplaceAll(activityInspectionColumns(j, s.lease.sources[j]), "a.", "b.")
			query := cte + ` SELECT ` + activityInspectionColumns(i, s.lease.sources[i]) + `,` + columnsB + fmt.Sprintf(` FROM s%d.runtime_activities a JOIN s%d.runtime_activities b ON b.subject_id=a.subject_id WHERE a.subject_id IN(SELECT subject_id FROM ids) AND a.kind IN('exchange.started','exchange.completed') AND b.kind IN('exchange.started','exchange.completed')`, i, j) + predicate
			rows, err := s.tx.QueryContext(s.lease.ctx, query, args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var a, b activity.Record
				var af, bf recording.Fence
				as, bs := activityScanState{record: &a}, activityScanState{record: &b}
				targets := append(as.targets(), &af.ArchiveEpoch, &af.CaptureGeneration)
				targets = append(targets, bs.targets()...)
				targets = append(targets, &bf.ArchiveEpoch, &bf.CaptureGeneration)
				if err = rows.Scan(targets...); err != nil {
					break
				}
				a, err = as.finish()
				if err != nil {
					break
				}
				b, err = bs.finish()
				if err != nil {
					break
				}
				if !sameExchangeIdentity(a, b) || activityScopeOf(a, af) != activityScopeOf(b, bf) || a.Kind == b.Kind && (i == j || !sameInspectedActivity(a, b, false)) {
					err = activity.ErrInvalidEvent
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
func (r *ActivityInspectionReader) SummarizeExchanges(ctx context.Context, q activity.SummaryScope) (resultValue activity.ExchangeSummary, resultErr error) {
	if r != nil {
		defer func(caller context.Context) { resultErr = r.catalog.classifyReadCancellation(caller, resultErr) }(ctx)
	}
	if q.Validate() != nil {
		return activity.ExchangeSummary{}, activity.ErrInvalidEvent
	}
	s, err := r.begin(ctx)
	if err != nil {
		return activity.ExchangeSummary{}, err
	}
	defer s.close()
	var cte string
	var args []any
	columns := `a.subject_id,a.status,a.reason_code,a.occurred_at_unix_ms`
	if q.SessionID != "" {
		cte, args, err = r.sessionIDs(s, q)
		if err == nil {
			cte, args, err = s.canonicalFromIDs(activity.PageRequest{}, columns, cte, args, true)
		}
	} else {
		cte, args, err = s.canonicalRelation(activity.PageRequest{CaptureRunID: q.CaptureRunID, ManualCaptureID: q.ManualCaptureID}, columns)
	}
	if err != nil {
		return activity.ExchangeSummary{}, err
	}
	rows, err := s.tx.QueryContext(s.lease.ctx, cte+` SELECT status,count(*),min(occurred_at_unix_ms),max(occurred_at_unix_ms),max(length(CAST(reason_code AS BLOB))) FROM canonical GROUP BY status`, args...)
	if err != nil {
		return activity.ExchangeSummary{}, err
	}
	result := activity.ExchangeSummary{Scope: q, GeneratedAt: time.Now().UTC(), Failures: []activity.FailureCount{}}
	for rows.Next() {
		var status activity.Status
		var reasonBytes int
		var count int
		var first, last int64
		if err = rows.Scan(&status, &count, &first, &last, &reasonBytes); err != nil {
			break
		}
		if count <= 0 || !validActivityMillis(first) || !validActivityMillis(last) || reasonBytes > 512 {
			err = ErrInspectionUnavailable
			break
		}
		result.Requests += count
		switch status {
		case activity.StatusSucceeded:
			result.Succeeded += count
		case activity.StatusPending:
			result.Pending += count
		case activity.StatusCanceled:
			result.Canceled += count
		case activity.StatusFailed:
			result.Failed += count
		default:
			err = ErrInspectionUnavailable
		}
		if err != nil {
			break
		}
		a, b := time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC()
		if result.FirstObservedAt == nil || a.Before(*result.FirstObservedAt) {
			result.FirstObservedAt = &a
		}
		if result.LastObservedAt == nil || b.After(*result.LastObservedAt) {
			result.LastObservedAt = &b
		}
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return activity.ExchangeSummary{}, err
	}
	result.OtherFailures = result.Failed
	if result.Failed > 0 {
		rows, err = s.tx.QueryContext(s.lease.ctx, cte+` SELECT reason_code,count(*) AS failures FROM canonical WHERE status='failed' GROUP BY reason_code ORDER BY failures DESC,reason_code ASC LIMIT 10`, args...)
		if err != nil {
			return activity.ExchangeSummary{}, err
		}
		for rows.Next() {
			var f activity.FailureCount
			if err = rows.Scan(&f.ReasonCode, &f.Count); err != nil {
				break
			}
			if f.Count <= 0 || f.Count > result.OtherFailures {
				err = ErrInspectionUnavailable
				break
			}
			result.OtherFailures -= f.Count
			result.Failures = append(result.Failures, f)
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return activity.ExchangeSummary{}, err
		}
	}
	var expiry time.Time
	if q.SessionID == "" {
		available, expires, bodyErr := r.launchBodies(s, q)
		if bodyErr != nil {
			return activity.ExchangeSummary{}, bodyErr
		}
		result.ContentBodiesAvailable = &available
		expiry = expires
		result.InspectionExpiresAt = expiry
	}
	s.endSQL()
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return activity.ExchangeSummary{}, err
	}
	if !expiry.IsZero() && !expiry.After(time.Now().UTC()) {
		return activity.ExchangeSummary{}, ErrInspectionChanged
	}
	return result, nil
}
func (r *ActivityInspectionReader) launchBodies(s *activityInspectionRead, q activity.SummaryScope) (bool, time.Time, error) {
	kind, id := "managed_run", q.CaptureRunID
	if q.ManualCaptureID != "" {
		kind, id = "manual_capture", q.ManualCaptureID
	}
	after := ""
	for {
		projections := []string{}
		args := []any{}
		for ordinal := range s.lease.sources {
			projections = append(projections, fmt.Sprintf(`SELECT exchange_id FROM s%d.runtime_exchange_contents WHERE scope_kind=? AND scope_id=? AND mode='full' AND expires_at_unix_ms>? AND exchange_id>?`, ordinal))
			args = append(args, kind, id, time.Now().UTC().UnixMilli(), after)
		}
		rows, err := s.tx.QueryContext(s.lease.ctx, `SELECT CASE WHEN length(CAST(exchange_id AS BLOB)) BETWEEN 1 AND 512 THEN exchange_id END FROM (`+strings.Join(projections, ` UNION `)+`) ORDER BY exchange_id LIMIT 256`, args...)
		if err != nil {
			return false, time.Time{}, err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return false, time.Time{}, err
		}
		for _, id := range ids {
			view := &contentInspectionRead{lease: s.lease, tx: s.tx, token: s.token, mirror: s.mirror}
			root, err := r.content.selectContent(view, id, nil)
			if errors.Is(err, exchangecontent.ErrNotFound) {
				continue
			}
			if err != nil {
				return false, time.Time{}, err
			}
			if root.reference.manifest.Mode == environment.ContentRecordingFull {
				return true, root.reference.manifest.ExpiresAt, nil
			}
		}
		if len(ids) < 256 {
			return false, time.Time{}, nil
		}
		after = ids[len(ids)-1]
	}
}

func validActivityMillis(value int64) bool {
	return value > -62135596800000 && value <= 253402300799999
}

func activityDisplaySQL(ordinal int, source InspectionSource, name string) string {
	if source.Current {
		return "a." + name
	}
	if name == "capture_label" {
		return fmt.Sprintf(`COALESCE((SELECT c.executable_label FROM s%d.capture_runs c WHERE c.run_id=a.capture_run_id),(SELECT m.display_name FROM s%d.manual_captures m WHERE m.capture_id=a.manual_capture_id),'')`, ordinal, ordinal)
	}
	return fmt.Sprintf(`COALESCE((SELECT c.%s FROM s%d.capture_runs c WHERE c.run_id=a.capture_run_id),'')`, name, ordinal)
}
func activityProjectionColumns(ordinal int, source InspectionSource, columns string) string {
	for _, name := range []string{"workspace_id", "workspace_label", "capture_label"} {
		columns = strings.ReplaceAll(columns, "{{"+name+"}}", activityDisplaySQL(ordinal, source, name))
	}
	epoch, generation, kind, id := activityScopeSQL(source, "a.")
	return strings.NewReplacer("{{epoch}}", epoch, "{{generation}}", generation, "{{scope_kind}}", kind, "{{scope_id}}", id).Replace(columns)
}
