package runtimepersistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type NativeInspectionReader struct {
	catalog    *InspectionCatalog
	visibility recording.InspectionVisibility
}

func NewNativeInspectionReader(c *InspectionCatalog, v recording.InspectionVisibility) *NativeInspectionReader {
	return &NativeInspectionReader{c, v}
}
func (r *NativeInspectionReader) Check(ctx context.Context, t recording.InspectionToken) error {
	return NewInspectionBoundary(r.visibility, r.catalog).Check(ctx, t)
}

type nativeInspectionRead struct {
	lease    *inspectionLease
	tx       *sql.Tx
	snapshot recording.VisibilitySnapshot
	token    recording.InspectionToken
}

func (r *NativeInspectionReader) begin(ctx context.Context, c captureidentity.Reference) (*nativeInspectionRead, error) {
	if c.Validate() != nil {
		return nil, nativetool.ErrInvalid
	}
	if r == nil || r.catalog == nil || r.visibility == nil {
		return nil, ErrInspectionUnavailable
	}
	snapshot, err := r.visibility.ReadInspectionSnapshot(ctx, []captureidentity.Reference{c})
	if err != nil {
		return nil, err
	}
	if !snapshot.Stamp.Valid() || snapshot.Stamp.LayoutID != r.catalog.layout {
		return nil, ErrInspectionUnavailable
	}
	lease, err := r.catalog.acquire(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := lease.conn.BeginTx(lease.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		lease.close()
		return nil, err
	}
	return &nativeInspectionRead{lease: lease, tx: tx, snapshot: snapshot, token: recording.InspectionToken{Control: snapshot.Stamp, Sources: lease.version}}, nil
}
func (s *nativeInspectionRead) close() {
	if s.tx != nil {
		s.tx.Rollback()
		s.tx = nil
	}
	s.lease.close()
}
func nativeInspectionQuery(c captureidentity.Reference, stamp recording.InspectionStamp) string {
	b, _ := json.Marshal(struct {
		Capture captureidentity.Reference
		Stamp   recording.InspectionStamp
	}{c, stamp})
	return string(b)
}
func nativeBodyQuery(q nativetool.InspectionContentRequest) string {
	b, _ := json.Marshal(struct {
		Capture captureidentity.Reference
		ID      nativetool.ID
		Part    nativetool.ContentPart
		Digest  nativetool.Digest
		Expiry  int64
		Stamp   recording.InspectionStamp
	}{q.Capture, q.OperationID, q.Part, q.Descriptor.Digest, q.Descriptor.ExpiresAt.UnixMilli(), q.Token.Control})
	return string(b)
}
func (r *NativeInspectionReader) Get(ctx context.Context, c captureidentity.Reference, id nativetool.ID) (nativetool.InspectionOperation, error) {
	if !nativeID(id) {
		return nativetool.InspectionOperation{}, nativetool.ErrInvalid
	}
	s, err := r.begin(ctx, c)
	if err != nil {
		return nativetool.InspectionOperation{}, err
	}
	defer s.close()
	op, err := r.get(s, c, id, time.Now().UTC())
	s.close()
	if err != nil {
		return nativetool.InspectionOperation{}, err
	}
	if err = r.Check(ctx, s.token); err != nil {
		return nativetool.InspectionOperation{}, err
	}
	return op, nil
}

// Discovery selects one canonical publication per ID before the continuation
// predicate. Event rows in released archives are positions, not snapshots.
func (r *NativeInspectionReader) Page(ctx context.Context, q nativetool.InspectionPageRequest) (nativetool.InspectionPage, error) {
	result := nativetool.InspectionPage{Items: []nativetool.InspectionOperation{}}
	if q.Limit < 1 || q.Limit > 199 {
		return result, nativetool.ErrInvalid
	}
	s, err := r.begin(ctx, q.Capture)
	if err != nil {
		return result, err
	}
	defer s.close()
	query := nativeInspectionQuery(q.Capture, s.snapshot.Stamp)
	position := inspectionPosition{}
	if q.Cursor != "" {
		position, err = r.catalog.decodeCursor("native-page", query, q.Cursor)
		if err != nil {
			return result, err
		}
	}
	projections := []string{}
	args := []any{}
	for ordinal, source := range s.lease.sources {
		alias := fmt.Sprintf("s%d", ordinal)
		if source.Current {
			projections = append(projections, fmt.Sprintf(`SELECT %d AS source,position,operation_id FROM %s.native_inspection_index WHERE capture_kind=? AND capture_id=?`, ordinal, alias))
		} else if publishedNativeRevision(source.Database.Revision) {
			projections = append(projections, fmt.Sprintf(`SELECT %d AS source,MAX(e.sequence) AS position,o.operation_id FROM %s.native_tool_operations o JOIN %s.native_tool_events e ON e.operation_id=o.operation_id WHERE o.capture_kind=? AND o.capture_id=? GROUP BY o.operation_id`, ordinal, alias, alias))
		} else {
			continue
		}
		args = append(args, q.Capture.Kind, q.Capture.ID)
	}
	// One current source always exists. Bound each publication batch and advance
	// across filtered rows; the lease's deadline bounds repeated batches.
	sqlText := `WITH candidates AS (` + strings.Join(projections, " UNION ALL ") + `), ranked AS (SELECT *,row_number() OVER(PARTITION BY operation_id ORDER BY source DESC,position DESC) AS winner FROM candidates) SELECT source,position,operation_id FROM ranked WHERE winner=1 AND (source>? OR (source=? AND position>?)) ORDER BY source,position LIMIT 200`
	now := time.Now().UTC()
	for len(result.Items) < q.Limit {
		batchArgs := append(append([]any{}, args...), position.Ordinal, position.Ordinal, position.Boundary)
		rows, err := s.tx.QueryContext(s.lease.ctx, sqlText, batchArgs...)
		if err != nil {
			return result, err
		}
		type publication struct {
			position inspectionPosition
			id       nativetool.ID
		}
		batch := []publication{}
		for rows.Next() {
			var v publication
			if err = rows.Scan(&v.position.Ordinal, &v.position.Boundary, &v.id); err != nil {
				rows.Close()
				return result, err
			}
			batch = append(batch, v)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return result, err
		}
		if len(batch) == 0 {
			currentOrdinal := uint32(len(s.lease.sources) - 1)
			if position.Ordinal != currentOrdinal {
				position = inspectionPosition{Ordinal: currentOrdinal}
			}
			break
		}
		for _, v := range batch {
			position = v.position
			op, err := r.get(s, q.Capture, v.id, now)
			if errors.Is(err, nativetool.ErrNotFound) {
				continue
			}
			if err != nil {
				return result, err
			}
			result.Items = append(result.Items, op)
			if len(result.Items) == q.Limit {
				break
			}
		}
	}
	s.close()
	result.NextCursor, err = r.catalog.encodeCursor("native-page", query, position)
	if err != nil {
		return nativetool.InspectionPage{}, err
	}
	result.Token = s.token
	if err = r.Check(ctx, s.token); err != nil {
		return nativetool.InspectionPage{}, err
	}
	return result, nil
}
func legacyNativeHistorySQL(alias string) string {
	// Only the event at the stored operation revision describes its retained
	// snapshot. Earlier references never fabricate earlier operation states.
	return `SELECT o.operation_id,o.revision,o.capture_kind,o.capture_id,1,1,o.environment_id,o.environment_revision,o.environment_digest,b.binding_digest,a.approval_id,o.recording,o.retention_days,o.rpc_kind,coalesce(b.tool_name,''),o.target_origin,CASE WHEN b.operation_id IS NULL THEN '' ELSE coalesce(json_extract(CAST(env.payload_json AS TEXT),'$.clientToolAccess.route.credentialOwner'),'') END,o.state,o.decision_source,o.terminal_reason,o.created_at_unix_ms,o.completed_at_unix_ms,o.bytes_in,o.bytes_out,e.kind,e.occurred_at_unix_ms,(SELECT MAX(occurred_at_unix_ms) FROM ` + alias + `.native_tool_events WHERE operation_id=o.operation_id),o.metadata_expires_at_unix_ms FROM ` + alias + `.native_tool_operations o JOIN ` + alias + `.native_tool_events e ON e.operation_id=o.operation_id AND e.operation_revision=o.revision LEFT JOIN ` + alias + `.native_tool_bindings b ON b.operation_id=o.operation_id LEFT JOIN ` + alias + `.native_tool_approvals a ON a.operation_id=o.operation_id LEFT JOIN ` + alias + `.environment_revisions env ON env.environment_id=o.environment_id AND env.revision=o.environment_revision WHERE o.operation_id=?`
}
func nativeIdentity(op nativetool.Operation) nativetool.InspectionIdentity {
	return nativetool.InspectionIdentity{OperationID: op.ID, Capture: op.Capture, EnvironmentID: op.EnvironmentID, EnvironmentRevision: op.EnvironmentRevision, EnvironmentDigest: op.EnvironmentDigest, Kind: op.Kind, TargetOrigin: op.TargetOrigin, Recording: op.Recording, CreatedAt: op.CreatedAt}
}
func (r *NativeInspectionReader) get(s *nativeInspectionRead, c captureidentity.Reference, id nativetool.ID, now time.Time) (nativetool.InspectionOperation, error) {
	result := nativetool.InspectionOperation{Token: s.token}
	if fence, exists := s.snapshot.Scopes[c]; !exists || fence.Deleted {
		return result, nativetool.ErrNotFound
	}
	var identity *nativetool.InspectionIdentity
	var scope recording.Scope
	var history *nativetool.RecordedHistory
	type partValue struct {
		descriptor nativetool.InspectionContentDescriptor
		ordinal    int
		identity   nativetool.ContentIdentity
	}
	parts := map[nativetool.ContentPart]partValue{}
	acceptPart := func(part nativetool.ContentPart, v partValue) error {
		for _, previous := range parts {
			if previous.identity.OperationID != "" && v.identity.OperationID != "" && previous.identity != v.identity {
				return nativetool.ErrConflict
			}
		}
		if previous, exists := parts[part]; exists && previous.descriptor.ContentDescriptor != v.descriptor.ContentDescriptor {
			return nativetool.ErrConflict
		}
		parts[part] = v
		return nil
	}
	accept := func(i nativetool.InspectionIdentity, sc recording.Scope) error {
		if validateNativeInspectionIdentity(i, sc) != nil || i.Capture != c {
			return nativetool.ErrInvalid
		}
		if identity != nil && (*identity != i || scope != sc) {
			return nativetool.ErrConflict
		}
		identity = &i
		scope = sc
		return nil
	}
	for ordinal, source := range s.lease.sources {
		alias := fmt.Sprintf("s%d", ordinal)
		if !source.Current && !publishedNativeRevision(source.Database.Revision) {
			continue
		}
		historySQL := legacyNativeHistorySQL(alias)
		if source.Current {
			index, sc, err := scanNativeIndex(s.tx.QueryRowContext(s.lease.ctx, `SELECT `+nativeIndexColumns+` FROM `+alias+`.native_inspection_index WHERE operation_id=?`, id))
			if err == nil {
				if err = accept(index.Identity, sc); err != nil {
					return result, err
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return result, err
			}
			historySQL = `SELECT ` + nativeHistoryColumns + ` FROM ` + alias + `.native_history_snapshots WHERE operation_id=? ORDER BY operation_revision DESC LIMIT 1`
		}
		h, err := scanNativeHistory(s.tx.QueryRowContext(s.lease.ctx, historySQL, id))
		if err == nil {
			if err = accept(nativeIdentity(h.Observation.Operation), h.Scope); err != nil {
				return result, err
			}
			if history != nil && history.Observation.Operation.Revision == h.Observation.Operation.Revision && !reflect.DeepEqual(*history, h) {
				return result, nativetool.ErrConflict
			}
			if history == nil || h.Observation.Operation.Revision >= history.Observation.Operation.Revision {
				history = &h
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return result, err
		}
		if source.Current {
			rows, err := s.tx.QueryContext(s.lease.ctx, `SELECT `+nativeContentColumns+` FROM `+alias+`.native_tool_contents WHERE operation_id=?`, id)
			if err != nil {
				return result, err
			}
			for rows.Next() {
				v, digest, err := scanNativeContent(rows)
				if err != nil {
					rows.Close()
					return result, err
				}
				if err = accept(v.Identity.InspectionIdentity(), v.Identity.Scope); err != nil {
					rows.Close()
					return result, err
				}
				var d nativetool.Digest
				copy(d[:], digest)
				at := v.ObservedAt
				if err = acceptPart(v.Part, partValue{descriptor: nativetool.InspectionContentDescriptor{ContentDescriptor: nativetool.ContentDescriptor{Digest: d, ExpiresAt: v.Identity.ExpiresAt}, ObservedAt: &at}, ordinal: ordinal, identity: v.Identity}); err != nil {
					rows.Close()
					return result, err
				}
			}
			if err = errors.Join(rows.Err(), rows.Close()); err != nil {
				return result, err
			}
		} else {
			rows, err := s.tx.QueryContext(s.lease.ctx, `SELECT part,body_digest,expires_at_unix_ms FROM `+alias+`.native_tool_contents WHERE operation_id=?`, id)
			if err != nil {
				return result, err
			}
			for rows.Next() {
				var part nativetool.ContentPart
				var digest []byte
				var expiry int64
				if err = rows.Scan(&part, &digest, &expiry); err != nil {
					rows.Close()
					return result, err
				}
				if len(digest) != 32 || !nativeContentPart(string(part)) || h.Observation.Operation.ID == "" || h.Observation.Operation.Recording.RetentionDays == 0 || expiry != h.Observation.Operation.CreatedAt.Add(time.Duration(h.Observation.Operation.Recording.RetentionDays)*24*time.Hour).UnixMilli() {
					rows.Close()
					return result, nativetool.ErrInvalid
				}
				var d nativetool.Digest
				copy(d[:], digest)
				p := h.Observation.Operation
				contentIdentity := nativetool.ContentIdentity{OperationID: p.ID, Scope: h.Scope, EnvironmentID: p.EnvironmentID, EnvironmentRevision: p.EnvironmentRevision, EnvironmentDigest: p.EnvironmentDigest, Kind: p.Kind, ToolName: p.ToolName, TargetOrigin: p.TargetOrigin, CredentialOwner: p.CredentialOwner, Recording: p.Recording, CreatedAt: p.CreatedAt, ExpiresAt: time.UnixMilli(expiry).UTC()}
				if err = acceptPart(part, partValue{descriptor: nativetool.InspectionContentDescriptor{ContentDescriptor: nativetool.ContentDescriptor{Digest: d, ExpiresAt: time.UnixMilli(expiry).UTC()}}, ordinal: ordinal, identity: contentIdentity}); err != nil {
					rows.Close()
					return result, err
				}
			}
			if err = errors.Join(rows.Err(), rows.Close()); err != nil {
				return result, err
			}
		}
	}
	if identity == nil || !s.snapshot.Allows(scope) {
		return result, nativetool.ErrNotFound
	}
	result.Operation = nativetool.Operation{ID: id, Capture: c, RecordingScope: scope, EnvironmentID: identity.EnvironmentID, EnvironmentRevision: identity.EnvironmentRevision, EnvironmentDigest: identity.EnvironmentDigest, Kind: identity.Kind, TargetOrigin: identity.TargetOrigin, Recording: identity.Recording, CreatedAt: identity.CreatedAt, State: "unknown"}
	known := history != nil && history.Observation.MetadataExpiresAt.After(now)
	if known {
		result.Operation = history.Observation.Operation
	}
	for part, v := range parts {
		if !known && !v.descriptor.ExpiresAt.After(now) {
			continue
		}
		if v.identity.OperationID != "" {
			if known && (result.Operation.ToolName != v.identity.ToolName || result.Operation.CredentialOwner != v.identity.CredentialOwner) {
				return result, nativetool.ErrConflict
			}
			if !known {
				result.Operation.ToolName = v.identity.ToolName
				result.Operation.CredentialOwner = v.identity.CredentialOwner
			}
		}
		request := nativetool.InspectionContentRequest{Token: s.token, Capture: c, OperationID: id, Part: part, Descriptor: v.descriptor}
		ref, err := r.catalog.encodeCursor("native-body", nativeBodyQuery(request), inspectionPosition{Ordinal: uint32(v.ordinal)})
		if err != nil {
			return result, err
		}
		v.descriptor.Reference = ref
		if part == nativetool.ContentToolArguments {
			result.Arguments = &v.descriptor
		} else {
			result.Result = &v.descriptor
		}
	}
	if !known && result.Arguments == nil && result.Result == nil {
		return result, nativetool.ErrNotFound
	}
	return result, nil
}

// Namespacing only rewrites the two closed evidence relations. It retains all
// bounded BLOB projections, chunk/manifest checks and indexed digest equality.
type inspectionEvidenceQueryer struct {
	tx    *sql.Tx
	alias string
}

func (q inspectionEvidenceQueryer) query(sqlText string) string {
	return strings.NewReplacer("runtime_evidence_bodies", q.alias+".runtime_evidence_bodies", "runtime_evidence_chunks", q.alias+".runtime_evidence_chunks").Replace(sqlText)
}
func (q inspectionEvidenceQueryer) QueryRowContext(ctx context.Context, s string, args ...any) *sql.Row {
	return q.tx.QueryRowContext(ctx, q.query(s), args...)
}
func (q inspectionEvidenceQueryer) QueryContext(ctx context.Context, s string, args ...any) (*sql.Rows, error) {
	return q.tx.QueryContext(ctx, q.query(s), args...)
}
func (r *NativeInspectionReader) ReadContent(ctx context.Context, q nativetool.InspectionContentRequest) ([]byte, error) {
	if !nativeID(q.OperationID) || !nativeContentPart(string(q.Part)) || q.Offset < 0 || q.Limit < 1 || q.Limit > 128<<10 {
		return nil, nativetool.ErrInvalid
	}
	if err := r.Check(ctx, q.Token); err != nil {
		return nil, err
	}
	position, err := r.catalog.decodeCursor("native-body", nativeBodyQuery(q), q.Descriptor.Reference)
	if err != nil {
		return nil, err
	}
	s, err := r.begin(ctx, q.Capture)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if s.token != q.Token {
		return nil, ErrInspectionChanged
	}
	op, err := r.get(s, q.Capture, q.OperationID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	actual := op.Result
	if q.Part == nativetool.ContentToolArguments {
		actual = op.Arguments
	}
	if actual == nil || !actual.ExpiresAt.After(time.Now().UTC()) || actual.Reference != q.Descriptor.Reference || actual.ContentDescriptor != q.Descriptor.ContentDescriptor {
		return nil, ErrInspectionChanged
	}
	body, err := loadEvidenceBody(s.lease.ctx, inspectionEvidenceQueryer{s.tx, fmt.Sprintf("s%d", position.Ordinal)}, q.Descriptor.Digest[:])
	if err != nil {
		return nil, err
	}
	defer clear(body)
	if len(body) > 16<<20 || q.Offset > len(body) {
		return nil, nativetool.ErrInvalid
	}
	result := bytes.Clone(body[q.Offset:min(len(body), q.Offset+q.Limit)])
	s.close()
	if err = r.Check(ctx, s.token); err != nil {
		clear(result)
		return nil, err
	}
	if !actual.ExpiresAt.After(time.Now().UTC()) {
		clear(result)
		return nil, ErrInspectionChanged
	}
	return result, nil
}

var _ nativetool.InspectionReader = (*NativeInspectionReader)(nil)
