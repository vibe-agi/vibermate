package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"reflect"
	"strings"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type ActivityInspectionReader struct {
	catalog    *InspectionCatalog
	visibility recording.InspectionVisibility
	content    *ContentInspectionReader
}

func NewActivityInspectionReader(c *InspectionCatalog, v recording.InspectionVisibility, limits exchangecontent.SourceLimits) (*ActivityInspectionReader, error) {
	content, err := NewContentInspectionReader(c, v, limits)
	if err != nil {
		return nil, err
	}
	return &ActivityInspectionReader{catalog: c, visibility: v, content: content}, nil
}
func (r *ActivityInspectionReader) Begin(ctx context.Context) (recording.InspectionToken, error) {
	return NewInspectionBoundary(r.visibility, r.catalog).Begin(ctx)
}
func (r *ActivityInspectionReader) Check(ctx context.Context, t recording.InspectionToken) error {
	return NewInspectionBoundary(r.visibility, r.catalog).Check(ctx, t)
}

type activityInspectionRead struct {
	lease  *inspectionLease
	tx     *sql.Tx
	token  recording.InspectionToken
	mirror inspectionMirror
	cancel context.CancelFunc
	// visible memoizes fence lookups for this read. The read-only transaction
	// is one snapshot, so every row of a Capture generation has one answer.
	visible map[recording.Scope]bool
}

func (s *activityInspectionRead) endSQL() {
	if s.tx != nil {
		s.tx.Rollback()
		s.tx = nil
	}
}
func (s *activityInspectionRead) close() { s.endSQL(); s.lease.close(); s.cancel() }
func (r *ActivityInspectionReader) begin(ctx context.Context) (*activityInspectionRead, error) {
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
	s := &activityInspectionRead{lease: lease, cancel: cancel, token: recording.InspectionToken{Control: snapshot.Stamp, Sources: lease.version}}
	s.tx, err = lease.conn.BeginTx(lease.ctx, &sql.TxOptions{ReadOnly: true})
	if err == nil {
		s.mirror, err = readInspectionMirror(lease.ctx, s.tx, fmt.Sprintf("s%d", len(lease.sources)-1), snapshot.Stamp)
	}
	if err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}
func (s *activityInspectionRead) allows(scope recording.Scope) (bool, error) {
	if scope.Fence == (recording.Fence{}) {
		return false, nil
	}
	if !validOriginalRecordingScope(scope) {
		return false, ErrInspectionUnavailable
	}
	if scope.Fence.ArchiveEpoch != s.token.Control.ArchiveEpoch {
		return false, nil
	}
	if scope.Capture == (captureidentity.Reference{}) {
		return true, nil
	}
	if visible, ok := s.visible[scope]; ok {
		return visible, nil
	}
	var visible bool
	err := s.tx.QueryRowContext(s.lease.ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM s%d.inspection_mirror_fences WHERE mirror_generation=? AND capture_kind=? AND capture_id=? AND capture_generation=? AND deleted=0)`, len(s.lease.sources)-1), s.mirror.Generation, scope.Capture.Kind, scope.Capture.ID, scope.Fence.CaptureGeneration).Scan(&visible)
	if err != nil {
		return false, err
	}
	if s.visible == nil {
		s.visible = map[recording.Scope]bool{}
	}
	s.visible[scope] = visible
	return visible, nil
}

// Scalars are guarded before Go materialization. Legacy display joins are
// frozen source-local facts, never current authority or mutable control labels.
func activityInspectionColumns(ordinal int, source InspectionSource) string {
	numbers := map[string]bool{"sequence": true, "occurred_at_unix_ms": true, "environment_revision": true, "client_endpoint_revision": true, "protocol_plan_revision": true, "route_revision": true, "account_revision": true, "credential_epoch": true, "provider_status": true}
	columns := strings.Split(searchActivityColumns, ",")
	result := make([]string, 0, len(columns)+2)
	for _, column := range columns {
		name := strings.TrimPrefix(strings.TrimSpace(column), "candidate.")
		value := "a." + name
		if !source.Current {
			switch name {
			case "workspace_id", "workspace_label":
				value = fmt.Sprintf(`COALESCE((SELECT c.%s FROM s%d.capture_runs c WHERE c.run_id=a.capture_run_id),'')`, name, ordinal)
			case "capture_label":
				value = fmt.Sprintf(`COALESCE((SELECT c.executable_label FROM s%d.capture_runs c WHERE c.run_id=a.capture_run_id),(SELECT m.display_name FROM s%d.manual_captures m WHERE m.capture_id=a.manual_capture_id),'')`, ordinal, ordinal)
			}
		}
		switch {
		case numbers[name]:
			result = append(result, value)
		case name == "transport_evidence_json":
			result = append(result, `CASE WHEN `+value+` IS NULL OR length(`+value+`) BETWEEN 2 AND 65536 THEN `+value+` ELSE CAST('invalid transport' AS BLOB) END`)
		default:
			result = append(result, `CASE WHEN length(CAST(`+value+` AS BLOB))<=4096 THEN `+value+` END`)
		}
	}
	if source.Current {
		result = append(result, "a.recording_archive_epoch", "a.recording_capture_generation")
	} else {
		result = append(result, "1", `CASE WHEN a.capture_run_id<>'' OR a.manual_capture_id<>'' THEN 1 ELSE 0 END`)
	}
	return strings.Join(result, ",")
}

type locatedActivity struct {
	record   activity.Record
	scope    recording.Scope
	position inspectionPosition
}

func (s *activityInspectionRead) exchange(id string) (locatedActivity, error) {
	if id == "" || len(id) > activity.MaxIdentityBytes || strings.TrimSpace(id) != id {
		return locatedActivity{}, activity.ErrInvalidEvent
	}
	var winner *locatedActivity
	perKind := map[activity.Kind]locatedActivity{}
	for ordinal, source := range s.lease.sources {
		rows, err := s.tx.QueryContext(s.lease.ctx, `SELECT `+activityInspectionColumns(ordinal, source)+fmt.Sprintf(` FROM s%d.runtime_activities a WHERE a.subject_id=? AND a.kind IN('exchange.started','exchange.completed') ORDER BY a.sequence DESC LIMIT 3`, ordinal), id)
		if err != nil {
			return locatedActivity{}, err
		}
		localKinds := map[activity.Kind]bool{}
		for rows.Next() {
			candidate := locatedActivity{position: inspectionPosition{Ordinal: uint32(ordinal)}}
			state := activityScanState{record: &candidate.record}
			if err = rows.Scan(append(state.targets(), &candidate.scope.Fence.ArchiveEpoch, &candidate.scope.Fence.CaptureGeneration)...); err != nil {
				rows.Close()
				return locatedActivity{}, err
			}
			candidate.record, err = state.finish()
			if err != nil {
				rows.Close()
				return locatedActivity{}, err
			}
			candidate.position.Boundary = candidate.record.Sequence
			if candidate.record.CaptureRunID != "" {
				candidate.scope.Capture = captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: candidate.record.CaptureRunID}
			} else if candidate.record.ManualCaptureID != "" {
				candidate.scope.Capture = captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: candidate.record.ManualCaptureID}
			}
			if localKinds[candidate.record.Kind] {
				rows.Close()
				return locatedActivity{}, activity.ErrInvalidEvent
			}
			localKinds[candidate.record.Kind] = true
			if winner != nil && (!sameExchangeIdentity(winner.record, candidate.record) || winner.scope != candidate.scope) {
				rows.Close()
				return locatedActivity{}, activity.ErrInvalidEvent
			}
			if previous, exists := perKind[candidate.record.Kind]; exists && !sameInspectedActivity(previous.record, candidate.record, false) {
				rows.Close()
				return locatedActivity{}, activity.ErrInvalidEvent
			}
			perKind[candidate.record.Kind] = candidate
			if winner == nil || winner.record.Kind == activity.KindExchangeStarted && candidate.record.Kind == activity.KindExchangeCompleted || winner.record.Kind == candidate.record.Kind && candidate.position.Ordinal > winner.position.Ordinal {
				copy := candidate
				winner = &copy
			}
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return locatedActivity{}, err
		}
	}
	if winner == nil {
		return locatedActivity{}, activity.ErrExchangeNotFound
	}
	visible, err := s.allows(winner.scope)
	if err != nil {
		return locatedActivity{}, err
	}
	if !visible {
		return locatedActivity{}, activity.ErrExchangeNotFound
	}
	s.decorate(winner)
	return *winner, nil
}
func sameInspectedActivity(a, b activity.Record, event bool) bool {
	a.Sequence = 0
	b.Sequence = 0
	a.InspectionRoot = nil
	b.InspectionRoot = nil
	a.InspectionCursor = ""
	b.InspectionCursor = ""
	if !event {
		a.ID = ""
		b.ID = ""
	}
	// Conversation projection may legitimately deepen. Legacy display fields
	// were frozen in its Capture table, not on the event itself.
	a.Conversation = nil
	b.Conversation = nil
	a.WorkspaceID = ""
	b.WorkspaceID = ""
	a.WorkspaceLabel = ""
	b.WorkspaceLabel = ""
	a.CaptureLabel = ""
	b.CaptureLabel = ""
	return reflect.DeepEqual(a, b)
}
func (r *ActivityInspectionReader) GetExchange(ctx context.Context, id string) (resultValue activity.Record, resultErr error) {
	if r != nil {
		defer func(caller context.Context) { resultErr = r.catalog.classifyReadCancellation(caller, resultErr) }(ctx)
	}
	s, err := r.begin(ctx)
	if err != nil {
		return activity.Record{}, err
	}
	defer s.close()
	got, err := s.exchange(id)
	if err != nil {
		return activity.Record{}, err
	}
	s.endSQL()
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return activity.Record{}, err
	}
	return got.record, nil
}
func activityBetterExchange(s *activityInspectionRead, ordinal int) string {
	clauses := make([]string, 0, len(s.lease.sources))
	for other := range s.lease.sources {
		tie := "0"
		if other > ordinal {
			tie = "1"
		} else if other == ordinal {
			tie = "w.sequence>a.sequence"
		}
		clauses = append(clauses, fmt.Sprintf(`NOT EXISTS(SELECT 1 FROM s%d.runtime_activities w WHERE w.subject_id=a.subject_id AND w.kind IN('exchange.started','exchange.completed') AND ((w.kind='exchange.completed' AND a.kind='exchange.started') OR (w.kind=a.kind AND (%s))))`, other, tie))
	}
	return strings.Join(clauses, " AND ")
}
func (r *ActivityInspectionReader) ListExchanges(ctx context.Context, q activity.PageRequest) (activity.Page, error) {
	return r.list(ctx, q, true)
}
func (r *ActivityInspectionReader) List(ctx context.Context, q activity.PageRequest) (activity.Page, error) {
	return r.list(ctx, q, false)
}
func (r *ActivityInspectionReader) list(ctx context.Context, q activity.PageRequest, exchanges bool) (resultValue activity.Page, resultErr error) {
	if r != nil {
		defer func(caller context.Context) { resultErr = r.catalog.classifyReadCancellation(caller, resultErr) }(ctx)
	}
	empty := activity.Page{Items: []activity.Record{}}
	if q.Validate() != nil {
		return empty, activity.ErrInvalidEvent
	}
	if q.BeforeSequence != 0 {
		return empty, ErrInspectionCursor
	}
	if q.WithoutLocalConversationIdentity && !exchanges {
		return empty, activity.ErrInvalidEvent
	}
	s, err := r.begin(ctx)
	if err != nil {
		return empty, err
	}
	defer s.close()
	encoded, _ := json.Marshal(struct {
		Query activity.PageRequest
		Stamp recording.InspectionStamp
	}{func() activity.PageRequest { v := q; v.Cursor = ""; return v }(), s.token.Control})
	query := string(encoded)
	domain := "activity-events"
	key := "activity_id"
	if exchanges {
		domain = "activity-exchanges"
		key = "subject_id"
	}
	var before *inspectionPosition
	if q.Cursor != "" {
		p, err := r.catalog.decodeCursor(domain, query, q.Cursor)
		if err != nil {
			return empty, err
		}
		before = &p
	}
	projections := []string{}
	args := []any{}
	for ordinal := range s.lease.sources {
		where := []string{activityBetterEvent(s, ordinal)}
		if exchanges {
			where = []string{`a.kind IN('exchange.started','exchange.completed')`, activityBetterExchange(s, ordinal)}
		}
		for _, filter := range []struct{ column, value string }{{"capture_run_id", q.CaptureRunID}, {"manual_capture_id", q.ManualCaptureID}, {"environment_id", q.EnvironmentID}, {"conversation_projection_id", q.ConversationProjectionID}} {
			if filter.value != "" {
				where = append(where, "a."+filter.column+"<>'' AND a."+filter.column+"=?")
				args = append(args, filter.value)
			}
		}
		if !q.OccurredAtOrAfter.IsZero() {
			where = append(where, "a.occurred_at_unix_ms>=? AND a.occurred_at_unix_ms<?")
			args = append(args, q.OccurredAtOrAfter.UnixMilli(), q.OccurredBefore.UnixMilli())
		}
		if before != nil {
			if uint32(ordinal) > before.Ordinal {
				where = append(where, "0")
			} else if uint32(ordinal) == before.Ordinal {
				where = append(where, "a.sequence<?")
				args = append(args, before.Boundary)
			}
		}
		projections = append(projections, fmt.Sprintf(`SELECT %d AS ordinal,a.sequence,CASE WHEN length(CAST(a.%s AS BLOB)) BETWEEN 1 AND 512 THEN a.%s END AS id FROM s%d.runtime_activities a WHERE `, ordinal, key, key, ordinal)+strings.Join(where, " AND "))
	}
	args = append(args, q.Limit+1)
	rows, err := s.tx.QueryContext(s.lease.ctx, strings.Join(projections, " UNION ALL ")+` ORDER BY ordinal DESC,sequence DESC LIMIT ?`, args...)
	if err != nil {
		return empty, err
	}
	type candidate struct {
		position inspectionPosition
		id       string
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.position.Ordinal, &c.position.Boundary, &c.id); err != nil {
			rows.Close()
			return empty, err
		}
		if c.position.Boundary <= 0 {
			rows.Close()
			return empty, ErrInspectionUnavailable
		}
		candidates = append(candidates, c)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return empty, err
	}
	more := len(candidates) > q.Limit
	if more {
		candidates = candidates[:q.Limit]
	}
	result := activity.Page{Items: []activity.Record{}, Consumed: len(candidates)}
	var consumed *inspectionPosition
	bytes := 0
	for _, c := range candidates {
		p := c.position
		consumed = &p
		read := s.event
		if exchanges {
			read = s.exchange
		}
		got, err := read(c.id)
		if errors.Is(err, activity.ErrExchangeNotFound) {
			continue
		}
		if err != nil {
			return empty, err
		}
		if got.position != c.position {
			return empty, ErrInspectionChanged
		}
		if q.WithoutLocalConversationIdentity {
			identity, identityErr := r.identity(s, got.record.SubjectID)
			if identityErr != nil && !errors.Is(identityErr, activity.ErrExchangeNotFound) {
				return empty, identityErr
			}
			if identityErr == nil && identity.Source == agentconversation.ClientIdentitySourceLocalState {
				continue
			}
		}
		got.record.InspectionCursor, err = r.catalog.encodeCursor(domain, query, got.position)
		if err != nil {
			return empty, err
		}
		encoded, err := json.Marshal(got.record)
		if err != nil {
			return empty, err
		}
		bytes += len(encoded) + activityInspectionPrivateBytes(got.record)
		if bytes > 8<<20 {
			return empty, ErrInspectionSourceLimit
		}
		result.Items = append(result.Items, got.record)
	}
	if more && consumed != nil {
		result.NextCursor, err = r.catalog.encodeCursor(domain, query, *consumed)
		if err != nil {
			return empty, err
		}
	}
	s.endSQL()
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return empty, err
	}
	return result, nil
}

func activityBetterEvent(s *activityInspectionRead, ordinal int) string {
	clauses := []string{"1"}
	for other := ordinal + 1; other < len(s.lease.sources); other++ {
		clauses = append(clauses, fmt.Sprintf(`NOT EXISTS(SELECT 1 FROM s%d.runtime_activities w WHERE w.activity_id=a.activity_id)`, other))
	}
	return strings.Join(clauses, " AND ")
}
func (s *activityInspectionRead) event(id string) (locatedActivity, error) {
	var winner *locatedActivity
	for ordinal, source := range s.lease.sources {
		rows, err := s.tx.QueryContext(s.lease.ctx, `SELECT `+activityInspectionColumns(ordinal, source)+fmt.Sprintf(` FROM s%d.runtime_activities a WHERE a.activity_id=? LIMIT 2`, ordinal), id)
		if err != nil {
			return locatedActivity{}, err
		}
		count := 0
		for rows.Next() {
			count++
			if count > 1 {
				rows.Close()
				return locatedActivity{}, activity.ErrInvalidEvent
			}
			candidate := locatedActivity{position: inspectionPosition{Ordinal: uint32(ordinal)}}
			state := activityScanState{record: &candidate.record}
			if err = rows.Scan(append(state.targets(), &candidate.scope.Fence.ArchiveEpoch, &candidate.scope.Fence.CaptureGeneration)...); err != nil {
				rows.Close()
				return locatedActivity{}, err
			}
			candidate.record, err = state.finish()
			if err != nil {
				rows.Close()
				return locatedActivity{}, err
			}
			candidate.position.Boundary = candidate.record.Sequence
			if candidate.record.CaptureRunID != "" {
				candidate.scope.Capture = captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: candidate.record.CaptureRunID}
			} else if candidate.record.ManualCaptureID != "" {
				candidate.scope.Capture = captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: candidate.record.ManualCaptureID}
			}
			if winner != nil && (winner.scope != candidate.scope || !sameInspectedActivity(winner.record, candidate.record, true)) {
				rows.Close()
				return locatedActivity{}, activity.ErrInvalidEvent
			}
			copy := candidate
			winner = &copy
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return locatedActivity{}, err
		}
	}
	if winner == nil {
		return locatedActivity{}, activity.ErrExchangeNotFound
	}
	visible, err := s.allows(winner.scope)
	if err != nil {
		return locatedActivity{}, err
	}
	if !visible {
		return locatedActivity{}, activity.ErrExchangeNotFound
	}
	s.decorate(winner)
	return *winner, nil
}

func (s *activityInspectionRead) decorate(value *locatedActivity) {
	if value.record.Kind != activity.KindExchangeStarted && value.record.Kind != activity.KindExchangeCompleted {
		return
	}
	source := s.lease.sources[value.position.Ordinal]
	value.record.InspectionRoot = &activity.InspectionRoot{SourceID: source.ID, Current: source.Current, Token: s.token, Scope: value.scope, Sequence: value.record.Sequence, ActivityID: value.record.ID, ExchangeID: value.record.SubjectID}
}

var _ activity.InspectionReader = (*ActivityInspectionReader)(nil)

func activityInspectionPrivateBytes(record activity.Record) int {
	size := len(record.InspectionCursor)
	if root := record.InspectionRoot; root != nil {
		size += int(unsafe.Sizeof(*root)) + len(root.SourceID) + len(root.Token.Control.LayoutID) + len(root.Token.Sources.SourceSet) + len(root.Scope.Capture.Kind) + len(root.Scope.Capture.ID) + len(root.ActivityID) + len(root.ExchangeID)
	}
	return size
}
