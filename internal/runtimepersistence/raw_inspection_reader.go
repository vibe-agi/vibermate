package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
)

const rawInspectionMaximumRows = 1024
const rawInspectionMaximumMetadata = 16 << 20
const rawInspectionMaximumListBytes = 8 << 20

type RawInspectionReader struct {
	catalog    *InspectionCatalog
	visibility recording.InspectionVisibility
	audit      rawevidence.LocatedRevealAuditor
	health     rawevidence.RecoveryReader
	now        func() time.Time
}

func NewRawInspectionReader(c *InspectionCatalog, v recording.InspectionVisibility, a rawevidence.LocatedRevealAuditor, h rawevidence.RecoveryReader) *RawInspectionReader {
	return &RawInspectionReader{catalog: c, visibility: v, audit: a, health: h, now: time.Now}
}
func (r *RawInspectionReader) Recovery() rawevidence.Recovery {
	if r.health != nil {
		return r.health.Recovery()
	}
	return rawevidence.Recovery{}
}
func (r *RawInspectionReader) Begin(ctx context.Context) (recording.InspectionToken, error) {
	return NewInspectionBoundary(r.visibility, r.catalog).Begin(ctx)
}
func (r *RawInspectionReader) Check(ctx context.Context, t recording.InspectionToken) error {
	return NewInspectionBoundary(r.visibility, r.catalog).Check(ctx, t)
}

type rawInspectionRead struct {
	lease   *inspectionLease
	tx      *sql.Tx
	token   recording.InspectionToken
	mirror  inspectionMirror
	current string
	cancel  context.CancelFunc
	now     func() time.Time
}

func (s *rawInspectionRead) endSQL() {
	if s.tx != nil {
		s.tx.Rollback()
		s.tx = nil
	}
}
func (s *rawInspectionRead) close() { s.endSQL(); s.lease.close(); s.cancel() }
func (r *RawInspectionReader) begin(ctx context.Context) (*rawInspectionRead, error) {
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
	s := &rawInspectionRead{lease: lease, token: recording.InspectionToken{Control: snapshot.Stamp, Sources: lease.version}, current: fmt.Sprintf("s%d", len(lease.sources)-1), cancel: cancel, now: r.now}
	s.tx, err = lease.conn.BeginTx(lease.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		s.close()
		return nil, err
	}
	s.mirror, err = readInspectionMirror(lease.ctx, s.tx, s.current, snapshot.Stamp)
	if err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

type locatedRawEnvelope struct {
	record       rawevidence.StoredEnvelope
	digest, salt []byte
	ordinal      int
}

func rawInspectionID(id string) bool {
	return len(id) > 0 && len(id) <= rawevidence.MaxIdentityBytes && utf8.ValidString(id) && !strings.ContainsAny(id, "\x00\r\n\t")
}

// Root identity is checked across sources before final filtering. Discovery
// and ranking carry only bounded IDs/source ordinals. Indexed pair checks avoid
// sorting or materializing retained payload metadata for a descriptor request.
func (s *rawInspectionRead) rows(column, id string, payload bool) ([]locatedRawEnvelope, error) {
	if column != "exchange_id" && column != "envelope_id" {
		return nil, ErrInspectionUnavailable
	}
	columns := strings.Split(rawEvidenceColumns, ",")
	for i := range columns {
		columns[i] = strings.TrimSpace(columns[i])
	}
	ids, publications := []string{}, []string{}
	args := []any{}
	alias := func(i int) string { return fmt.Sprintf("s%d", i) }
	salt := func(i int) string {
		return `(SELECT CASE WHEN length(salt)=32 THEN salt ELSE NULL END FROM ` + alias(i) + `.runtime_raw_evidence_redaction WHERE singleton=1)`
	}
	coordinates := func(i int, row string) (string, string) {
		if s.lease.sources[i].Current {
			return row + `.recording_archive_epoch`, row + `.recording_capture_generation`
		}
		return "1", `CASE WHEN ` + row + `.scope_kind='runtime' THEN 0 ELSE 1 END`
	}
	for i := range s.lease.sources {
		table := alias(i) + `.runtime_raw_evidence_envelopes`
		var count int
		if err := s.tx.QueryRowContext(s.lease.ctx, `SELECT count(*) FROM(SELECT 1 FROM `+table+` WHERE `+column+`=? LIMIT ?)`, id, rawInspectionMaximumRows+1).Scan(&count); err != nil {
			return nil, err
		}
		if count > rawInspectionMaximumRows {
			return nil, ErrInspectionSourceLimit
		}
		var invalid int
		err := s.tx.QueryRowContext(s.lease.ctx, `SELECT 1 FROM `+table+` WHERE `+column+`=? AND length(CAST(envelope_id AS BLOB)) NOT BETWEEN 1 AND 512 LIMIT 1`, id).Scan(&invalid)
		if err == nil {
			return nil, ErrInspectionUnavailable
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		ids = append(ids, `SELECT envelope_id FROM `+table+` WHERE `+column+`=?`)
		args = append(args, id)
		publications = append(publications, fmt.Sprintf(`SELECT %d AS source,envelope_id FROM %s WHERE envelope_id IN(SELECT envelope_id FROM ids)`, i, table))
	}
	cte := `WITH ids AS (` + strings.Join(ids, " UNION ") + `)`
	var count int
	if err := s.tx.QueryRowContext(s.lease.ctx, cte+` SELECT count(*) FROM(SELECT envelope_id FROM ids LIMIT `+fmt.Sprint(rawInspectionMaximumRows+1)+`)`, args...).Scan(&count); err != nil {
		return nil, err
	}
	if count > rawInspectionMaximumRows {
		return nil, ErrInspectionSourceLimit
	}
	for i := range s.lease.sources {
		for j := i + 1; j < len(s.lease.sources); j++ {
			join := ` FROM ` + alias(i) + `.runtime_raw_evidence_envelopes a JOIN ` + alias(j) + `.runtime_raw_evidence_envelopes b ON a.envelope_id=b.envelope_id WHERE a.envelope_id IN(SELECT envelope_id FROM ids) AND (`
			// Metadata above the supported reveal bound cannot be compared by
			// materializing arbitrary BLOBs; refuse ambiguity explicitly instead.
			var unsupported int
			err := s.tx.QueryRowContext(s.lease.ctx, cte+` SELECT 1`+join+fmt.Sprintf(`length(a.payload_metadata)>%d OR length(b.payload_metadata)>%d) LIMIT 1`, rawInspectionMaximumMetadata, rawInspectionMaximumMetadata), args...).Scan(&unsupported)
			if err == nil {
				return nil, ErrInspectionSourceLimit
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			differences := make([]string, 0, len(columns)+3)
			for _, name := range columns {
				differences = append(differences, `a.`+name+` IS NOT b.`+name)
			}
			ae, ag := coordinates(i, "a")
			be, bg := coordinates(j, "b")
			differences = append(differences, `(`+ae+`) IS NOT (`+be+`)`, `(`+ag+`) IS NOT (`+bg+`)`, salt(i)+` IS NOT `+salt(j))
			var conflict int
			err = s.tx.QueryRowContext(s.lease.ctx, cte+` SELECT 1`+join+strings.Join(differences, " OR ")+`) LIMIT 1`, args...).Scan(&conflict)
			if err == nil {
				return nil, fmt.Errorf("%w: conflicting Raw envelope identity", ErrInspectionUnavailable)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
	}
	cte += `, publications AS (` + strings.Join(publications, " UNION ALL ") + `), winners AS (SELECT envelope_id,MAX(source) AS source FROM publications GROUP BY envelope_id)`
	projections := []string{}
	for i := range s.lease.sources {
		selected := make([]string, 0, len(columns))
		for _, name := range columns {
			limit := 32768
			if name == "payload_metadata" {
				if !payload {
					selected = append(selected, `NULL AS payload_metadata`)
					continue
				}
				limit = rawInspectionMaximumMetadata
			}
			selected = append(selected, fmt.Sprintf(`CASE WHEN length(CAST(r.%s AS BLOB))<=%d THEN r.%s ELSE NULL END AS %s`, name, limit, name, name))
		}
		epoch, generation := coordinates(i, "r")
		projections = append(projections, `SELECT `+strings.Join(selected, ",")+fmt.Sprintf(`,%d AS source,`, i)+epoch+` AS recording_archive_epoch,`+generation+` AS recording_capture_generation,`+salt(i)+` AS original_salt FROM `+alias(i)+`.runtime_raw_evidence_envelopes r JOIN winners w ON w.envelope_id=r.envelope_id AND w.source=`+fmt.Sprint(i))
	}
	sqlText := cte + `, records AS (` + strings.Join(projections, " UNION ALL ") + `) SELECT ` + rawEvidenceColumns + `,r.source,r.recording_archive_epoch,r.recording_capture_generation,r.original_salt FROM records r WHERE r.` + column + `=? AND r.expires_at_unix_ms>? AND r.recording_archive_epoch=? AND ((r.scope_kind='runtime' AND r.scope_id='' AND r.recording_capture_generation=0) OR EXISTS(SELECT 1 FROM ` + s.current + `.inspection_mirror_fences f WHERE f.mirror_generation=? AND f.capture_kind=r.scope_kind AND f.capture_id=r.scope_id AND f.capture_generation=r.recording_capture_generation AND f.deleted=0)) ORDER BY r.observed_at_unix_ms,r.watermark,r.source,r.envelope_id LIMIT ?`
	args = append(args, id, s.now().UTC().UnixMilli(), s.token.Control.ArchiveEpoch, s.mirror.Generation, rawInspectionMaximumRows+1)
	rows, err := s.tx.QueryContext(s.lease.ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []locatedRawEnvelope{}
	bytes := 0
	for rows.Next() {
		var item locatedRawEnvelope
		var fence recording.Fence
		item.record, item.digest, err = scanRawEvidence(rows, &item.ordinal, &fence.ArchiveEpoch, &fence.CaptureGeneration, &item.salt)
		if err != nil {
			return nil, err
		}
		item.record.RecordingFence = fence
		if err = item.record.ValidateMetadata(); err != nil {
			return nil, err
		}
		if item.ordinal < 0 || item.ordinal >= len(s.lease.sources) || len(result) >= rawInspectionMaximumRows {
			return nil, ErrInspectionSourceLimit
		}
		encoded, err := json.Marshal(rawevidence.MetadataOf(item.record))
		if err != nil {
			return nil, err
		}
		bytes += len(encoded)
		if bytes > rawInspectionMaximumListBytes {
			return nil, ErrInspectionSourceLimit
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *RawInspectionReader) ListExchange(ctx context.Context, id string) ([]rawevidence.EnvelopeMetadata, error) {
	if !rawInspectionID(id) {
		return nil, rawevidence.ErrInvalidRead
	}
	s, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer s.close()
	records, err := s.rows("exchange_id", id, false)
	if err != nil {
		return nil, err
	}
	result := make([]rawevidence.EnvelopeMetadata, 0, len(records))
	for _, item := range records {
		result = append(result, rawevidence.MetadataOf(item.record))
	}
	s.endSQL()
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return nil, err
	}
	return result, nil
}
func (s *rawInspectionRead) envelope(id string) (locatedRawEnvelope, error, error) {
	records, err := s.rows("envelope_id", id, true)
	if err != nil {
		return locatedRawEnvelope{}, nil, err
	}
	if len(records) == 0 {
		return locatedRawEnvelope{}, nil, rawevidence.ErrEnvelopeNotFound
	}
	if len(records) != 1 {
		return locatedRawEnvelope{}, nil, ErrInspectionUnavailable
	}
	item := records[0]
	record, readErr := attachEvidenceBody(s.lease.ctx, inspectionEvidenceQueryer{s.tx, fmt.Sprintf("s%d", item.ordinal)}, item.record, item.digest)
	if readErr == nil {
		item.record = record
	}
	// Keep physical inspection ownership until credential/audit I/O actually
	// returns, but release every SQLite read lock before that external work.
	s.endSQL()
	return item, readErr, nil
}
func (r *RawInspectionReader) auditReveal(s *rawInspectionRead, item locatedRawEnvelope, actor string, readErr error) error {
	if r.audit == nil {
		return rawevidence.ErrPayloadUnavailable
	}
	outcome := rawevidence.RevealSucceeded
	if readErr != nil {
		outcome = rawevidence.RevealUnavailable
	}
	audit := rawevidence.LocatedRevealAudit{RevealAudit: rawevidence.RevealAudit{EnvelopeID: item.record.EnvelopeID, ExchangeID: item.record.ExchangeID, ActorID: actor, Outcome: outcome, OccurredAt: r.now().UTC()}, SourceID: s.lease.sources[item.ordinal].ID, Token: s.token, Scope: item.record.RecordingScope()}
	if err := r.audit.AppendLocatedRevealAudit(s.lease.ctx, audit); err != nil {
		return errors.Join(rawevidence.ErrPayloadUnavailable, err)
	}
	if err := r.Check(s.lease.ctx, s.token); err != nil {
		return err
	}
	if !item.record.ExpiresAt.After(r.now().UTC()) {
		return rawevidence.ErrEnvelopeNotFound
	}
	return nil
}
func (r *RawInspectionReader) Reveal(ctx context.Context, q rawevidence.RevealRequest) (rawevidence.RevealedEnvelope, error) {
	if err := q.Validate(); err != nil {
		return rawevidence.RevealedEnvelope{}, err
	}
	s, err := r.begin(ctx)
	if err != nil {
		return rawevidence.RevealedEnvelope{}, err
	}
	defer s.close()
	item, readErr, err := s.envelope(q.EnvelopeID)
	if err != nil {
		return rawevidence.RevealedEnvelope{}, err
	}
	var payload rawevidence.Payload
	if readErr == nil {
		payload, readErr = rawevidence.ReadStoredPayload(item.record, r.now().UTC())
	}
	if err = r.auditReveal(s, item, q.ActorID, readErr); err != nil {
		clear(item.record.Body)
		return rawevidence.RevealedEnvelope{}, err
	}
	if readErr != nil {
		clear(item.record.Body)
		return rawevidence.RevealedEnvelope{}, readErr
	}
	return rawevidence.RevealedEnvelope{Metadata: rawevidence.MetadataOf(item.record), Payload: payload}, nil
}
func (r *RawInspectionReader) RevealHeader(ctx context.Context, q rawevidence.HeaderRevealRequest, source providerauth.HeaderReader) (rawevidence.RevealedHeader, error) {
	if err := q.Validate(); err != nil {
		return rawevidence.RevealedHeader{}, err
	}
	s, err := r.begin(ctx)
	if err != nil {
		return rawevidence.RevealedHeader{}, err
	}
	defer s.close()
	item, readErr, err := s.envelope(q.EnvelopeID)
	if err != nil {
		return rawevidence.RevealedHeader{}, err
	}
	defer clear(item.record.Body)
	var result rawevidence.RevealedHeader
	if readErr == nil {
		redactor, err := rawevidence.NewRedactor(item.salt)
		if err != nil {
			readErr = rawevidence.ErrPayloadUnavailable
		} else {
			result, readErr = rawevidence.VerifyStoredHeader(s.lease.ctx, item.record, q.Name, source, redactor, r.now().UTC())
		}
	}
	if err = r.auditReveal(s, item, q.ActorID, readErr); err != nil {
		return rawevidence.RevealedHeader{}, err
	}
	if readErr != nil {
		return rawevidence.RevealedHeader{}, readErr
	}
	return result, nil
}
func (s *ObservationStore) AppendLocatedRevealAudit(ctx context.Context, a rawevidence.LocatedRevealAudit) error {
	if err := a.Validate(); err != nil {
		return err
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	result, err := s.store.database.ExecContext(operation, `INSERT INTO runtime_raw_inspection_reveal_audits(layout_id,source_set,source_generation,source_id,recording_scope_kind,recording_scope_id,recording_archive_epoch,recording_capture_generation,visibility_revision,usage_policy_revision,envelope_id,exchange_id,actor_id,outcome,occurred_at_unix_ms) SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,? FROM observation_storage_state WHERE singleton=1 AND layout_id=?`, a.Token.Control.LayoutID, a.Token.Sources.SourceSet, a.Token.Sources.Generation, a.SourceID, a.Scope.Capture.Kind, a.Scope.Capture.ID, a.Scope.Fence.ArchiveEpoch, a.Scope.Fence.CaptureGeneration, a.Token.Control.VisibilityRevision, a.Token.Control.UsagePolicyRevision, a.EnvelopeID, a.ExchangeID, a.ActorID, a.Outcome, a.OccurredAt.UnixMilli(), a.Token.Control.LayoutID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return rawevidence.ErrInvalidReveal
	}
	return nil
}

var _ rawevidence.Reader = (*RawInspectionReader)(nil)
var _ rawevidence.HeaderRevealer = (*RawInspectionReader)(nil)
var _ recording.InspectionBoundary = (*RawInspectionReader)(nil)

// Health belongs solely to the supplied current writer; archives never create
// sessions or contribute invented recovery/statistics values.
func (r *RawInspectionReader) WriterStatisticsAvailable() bool {
	_, ok := r.health.(interface{ Statistics() rawevidence.Statistics })
	return ok
}
func (r *RawInspectionReader) Statistics() rawevidence.Statistics {
	if health, ok := r.health.(interface{ Statistics() rawevidence.Statistics }); ok {
		return health.Statistics()
	}
	return rawevidence.Statistics{}
}
