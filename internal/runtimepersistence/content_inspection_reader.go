package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// contentReadQueries lets the existing bounded graph decoder run on its
// caller-owned read transaction. It has no mutation or connection-opening port.
type contentReadQueries interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Only our closed content SQL uses this adapter. Every graph table reference
// is qualified to ONE selected declared source; SQLite's attachment search
// order can never repair a missing node from another source.
type sourceContentQueries struct {
	tx      *sql.Tx
	qualify *strings.Replacer
}

func newSourceContentQueries(tx *sql.Tx, ordinal int) sourceContentQueries {
	pairs := []string{}
	for _, table := range []string{"runtime_exchange_contents", "runtime_exchange_content_transcripts", "runtime_exchange_content_messages", "runtime_exchange_content_blocks", "runtime_exchange_content_block_refs"} {
		pairs = append(pairs, table, fmt.Sprintf("s%d.%s", ordinal, table))
	}
	return sourceContentQueries{tx: tx, qualify: strings.NewReplacer(pairs...)}
}
func (q sourceContentQueries) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return q.tx.QueryContext(ctx, q.qualify.Replace(query), args...)
}
func (q sourceContentQueries) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.tx.QueryRowContext(ctx, q.qualify.Replace(query), args...)
}

type ContentInspectionReader struct {
	catalog    *InspectionCatalog
	visibility recording.InspectionVisibility
	limits     exchangecontent.SourceLimits
	now        func() time.Time
}

func NewContentInspectionReader(c *InspectionCatalog, v recording.InspectionVisibility, l exchangecontent.SourceLimits) (*ContentInspectionReader, error) {
	if c == nil || v == nil || l.Validate() != nil {
		return nil, ErrInspectionUnavailable
	}
	return &ContentInspectionReader{catalog: c, visibility: v, limits: l, now: func() time.Time { return time.Now().UTC() }}, nil
}
func (r *ContentInspectionReader) Begin(ctx context.Context) (recording.InspectionToken, error) {
	return NewInspectionBoundary(r.visibility, r.catalog).Begin(ctx)
}
func (r *ContentInspectionReader) Check(ctx context.Context, t recording.InspectionToken) error {
	return NewInspectionBoundary(r.visibility, r.catalog).Check(ctx, t)
}

type contentInspectionRead struct {
	lease  *inspectionLease
	tx     *sql.Tx
	token  recording.InspectionToken
	mirror inspectionMirror
	cancel context.CancelFunc
}

func (s *contentInspectionRead) endSQL() {
	if s.tx != nil {
		s.tx.Rollback()
		s.tx = nil
	}
}
func (s *contentInspectionRead) close() { s.endSQL(); s.lease.close(); s.cancel() }
func (r *ContentInspectionReader) begin(ctx context.Context) (*contentInspectionRead, error) {
	if ctx == nil {
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
	s := &contentInspectionRead{lease: lease, cancel: cancel, token: recording.InspectionToken{Control: snapshot.Stamp, Sources: lease.version}}
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

type locatedContent struct {
	reference storedContentReference
	ordinal   int
	scope     recording.Scope
	allowed   bool
}

func (r *ContentInspectionReader) selectContent(s *contentInspectionRead, id string, pinned *uint32) (locatedContent, error) {
	if id == "" || len(id) > exchangecontent.MaxExchangeIDBytes || !utf8.ValidString(id) || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\r\n\x00") {
		return locatedContent{}, exchangecontent.ErrInvalidEvidence
	}
	var winner, original *locatedContent
	for ordinal, source := range s.lease.sources {
		q := newSourceContentQueries(s.tx, ordinal)
		ref, err := loadStoredContentReferenceWithin(s.lease.ctx, q, id, time.UnixMilli(0).UTC(), &r.limits)
		if errors.Is(err, exchangecontent.ErrNotFound) {
			continue
		}
		if err != nil {
			return locatedContent{}, err
		}
		candidate := locatedContent{reference: ref, ordinal: ordinal}
		kind, owner := storedContentScope(ref.manifest.Parent)
		candidate.scope.Capture = captureidentity.Reference{Kind: captureidentity.Kind(kind), ID: owner}
		if source.Current {
			err = s.tx.QueryRowContext(s.lease.ctx, fmt.Sprintf(`SELECT recording_archive_epoch,recording_capture_generation FROM s%d.runtime_exchange_contents WHERE exchange_id=?`, ordinal), id).Scan(&candidate.scope.Fence.ArchiveEpoch, &candidate.scope.Fence.CaptureGeneration)
		} else {
			candidate.scope.Fence = recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}
			if candidate.scope.Capture == (captureidentity.Reference{}) {
				candidate.scope.Fence.CaptureGeneration = 0
			}
		}
		if err != nil {
			return locatedContent{}, err
		}
		if candidate.scope.Fence != (recording.Fence{}) {
			if !validOriginalRecordingScope(candidate.scope) {
				return locatedContent{}, ErrInspectionUnavailable
			}
			if candidate.scope.Fence.ArchiveEpoch == s.token.Control.ArchiveEpoch {
				if candidate.scope.Capture == (captureidentity.Reference{}) {
					candidate.allowed = true
				} else {
					err = s.tx.QueryRowContext(s.lease.ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM s%d.inspection_mirror_fences WHERE mirror_generation=? AND capture_kind=? AND capture_id=? AND capture_generation=? AND deleted=0)`, len(s.lease.sources)-1), s.mirror.Generation, candidate.scope.Capture.Kind, candidate.scope.Capture.ID, candidate.scope.Fence.CaptureGeneration).Scan(&candidate.allowed)
					if err != nil {
						return locatedContent{}, err
					}
				}
			}
		}
		if pinned != nil && uint32(ordinal) == *pinned {
			copy := candidate
			original = &copy
		}
		if winner != nil {
			if !sameContentOriginal(*winner, candidate) {
				return locatedContent{}, exchangecontent.ErrInvalidEvidence
			}
			// A terminal response precedes publication preference. Source-local base
			// presentation is intentionally excluded from immutable request identity.
			if winner.reference.manifest.Response != nil && candidate.reference.manifest.Response == nil {
				continue
			}
		}
		copy := candidate
		winner = &copy
	}
	if pinned != nil {
		if original == nil {
			return locatedContent{}, ErrInspectionChanged
		}
		winner = original
	}
	if winner == nil || !winner.allowed || !winner.reference.manifest.ExpiresAt.After(r.now()) {
		return locatedContent{}, exchangecontent.ErrNotFound
	}
	return *winner, nil
}
func sameContentOriginal(a, b locatedContent) bool {
	x, y := a.reference, b.reference
	xm, ym := x.manifest, y.manifest
	xr, yr := xm.Response, ym.Response
	xm.Response = nil
	ym.Response = nil
	return a.scope == b.scope && reflect.DeepEqual(xm, ym) && x.requestRoot == y.requestRoot && x.requestCount == y.requestCount && x.systemDigest == y.systemDigest && (xr == nil || yr == nil || reflect.DeepEqual(xr, yr) && x.expectedRoot == y.expectedRoot && x.expectedCount == y.expectedCount && x.responseDigest == y.responseDigest)
}
func (r *ContentInspectionReader) repository(s *contentInspectionRead, selected locatedContent) *exchangeContentRepository {
	repo := &exchangeContentRepository{reads: newSourceContentQueries(s.tx, selected.ordinal), operations: newOperationGate(), limits: &r.limits}
	repo.loadPhysical = repo.physicalContentRow
	return repo
}

func readInspectedContent[T any](r *ContentInspectionReader, ctx context.Context, id string, cursor string, read func(*contentInspectionRead, locatedContent, *exchangeContentRepository, string) (T, error)) (result T, err error) {
	s, err := r.begin(ctx)
	if err != nil {
		return result, err
	}
	defer s.close()
	var pinned *uint32
	inner := ""
	if cursor != "" {
		if len(cursor) > exchangecontent.MaxPageCursorBytes {
			return result, ErrInspectionCursor
		}
		proof, local, ok := strings.Cut(cursor, ".")
		if !ok {
			return result, ErrInspectionCursor
		}
		position, err := r.catalog.decodeCursor("semantic-content", contentCursorQuery(s.token, id, local), proof)
		if err != nil {
			return result, err
		}
		if position.Boundary != 0 {
			return result, ErrInspectionCursor
		}
		pinned = &position.Ordinal
		inner = local
	}
	selected, err := r.selectContent(s, id, pinned)
	if err != nil {
		return result, err
	}
	result, err = read(s, selected, r.repository(s, selected), inner)
	if err == nil {
		s.endSQL()
		err = r.Check(s.lease.ctx, s.token)
		if err == nil && !selected.reference.manifest.ExpiresAt.After(r.now()) {
			err = exchangecontent.ErrNotFound
		}
	}
	if err != nil {
		var empty T
		return empty, err
	}
	return result, nil
}
func contentCursorQuery(token recording.InspectionToken, id, inner string) string {
	encoded, _ := json.Marshal(struct {
		Stamp           recording.InspectionStamp
		Exchange, Inner string
	}{token.Control, id, inner})
	return string(encoded)
}
func (r *ContentInspectionReader) wrapCursor(s *contentInspectionRead, v locatedContent, cursor *string) error {
	if *cursor == "" {
		return nil
	}
	if _, err := decodeContentCursor(*cursor); err != nil {
		return err
	}
	proof, err := r.catalog.encodeCursor("semantic-content", contentCursorQuery(s.token, v.reference.manifest.ExchangeID, *cursor), inspectionPosition{Ordinal: uint32(v.ordinal)})
	if err != nil {
		return err
	}
	wrapped := proof + "." + *cursor
	if len(wrapped) > exchangecontent.MaxPageCursorBytes {
		return ErrInspectionSourceLimit
	}
	*cursor = wrapped
	return nil
}
func (r *ContentInspectionReader) wrapBlocks(s *contentInspectionRead, v locatedContent, blocks []exchangecontent.Block) error {
	for i := range blocks {
		if blocks[i].Deferred != nil {
			if err := r.wrapCursor(s, v, &blocks[i].Deferred.Cursor); err != nil {
				return err
			}
		}
	}
	return nil
}
func (r *ContentInspectionReader) wrapMessages(s *contentInspectionRead, v locatedContent, messages []exchangecontent.Message) error {
	for i := range messages {
		if err := r.wrapBlocks(s, v, messages[i].Blocks); err != nil {
			return err
		}
	}
	return nil
}
func (r *ContentInspectionReader) wrapProjection(s *contentInspectionRead, v locatedContent, p *exchangecontent.Projection) error {
	if err := r.wrapBlocks(s, v, p.Request.System); err != nil {
		return err
	}
	if err := r.wrapMessages(s, v, p.Request.Messages); err != nil {
		return err
	}
	if p.Response != nil {
		if err := r.wrapBlocks(s, v, p.Response.Blocks); err != nil {
			return err
		}
	}
	if p.Page != nil {
		for _, cursor := range []*string{&p.Page.RequestNextCursor, &p.Page.RequestEvidenceNextCursor, &p.Page.ResponseEvidenceNextCursor} {
			if err := r.wrapCursor(s, v, cursor); err != nil {
				return err
			}
		}
	}
	return p.ValidateWithin(s.lease.ctx, r.limits)
}
func (r *ContentInspectionReader) GetProjection(ctx context.Context, id string, view exchangecontent.RequestView) (exchangecontent.Projection, error) {
	return readInspectedContent(r, ctx, id, "", func(s *contentInspectionRead, v locatedContent, repo *exchangeContentRepository, _ string) (exchangecontent.Projection, error) {
		p, err := repo.GetProjection(s.lease.ctx, id, r.now(), view)
		if err == nil {
			err = r.wrapProjection(s, v, &p)
		}
		return p, err
	})
}
func (r *ContentInspectionReader) GetPagedProjection(ctx context.Context, id string, view exchangecontent.RequestView) (exchangecontent.Projection, error) {
	return readInspectedContent(r, ctx, id, "", func(s *contentInspectionRead, v locatedContent, repo *exchangeContentRepository, _ string) (exchangecontent.Projection, error) {
		p, err := repo.GetPagedProjection(s.lease.ctx, id, r.now(), view)
		if err == nil {
			err = r.wrapProjection(s, v, &p)
		}
		return p, err
	})
}
func (r *ContentInspectionReader) GetContentPage(ctx context.Context, id, cursor string) (exchangecontent.ContentPage, error) {
	if cursor == "" {
		return exchangecontent.ContentPage{}, ErrInspectionCursor
	}
	return readInspectedContent(r, ctx, id, cursor, func(s *contentInspectionRead, v locatedContent, repo *exchangeContentRepository, inner string) (exchangecontent.ContentPage, error) {
		p, err := repo.GetContentPage(s.lease.ctx, id, r.now(), inner)
		p.InspectionExpiresAt = v.reference.manifest.ExpiresAt
		if err != nil {
			return p, err
		}
		for _, cursor := range []*string{&p.NextCursor, &p.CanonicalCursor} {
			if err = r.wrapCursor(s, v, cursor); err != nil {
				return p, err
			}
		}
		if err = r.wrapBlocks(s, v, p.Blocks); err != nil {
			return p, err
		}
		err = r.wrapMessages(s, v, p.Messages)
		if err == nil {
			err = p.ValidateWithin(s.lease.ctx, r.limits)
		}
		return p, err
	})
}

func (r *ContentInspectionReader) Get(ctx context.Context, id string) (exchangecontent.Record, error) {
	return readInspectedContent(r, ctx, id, "", func(s *contentInspectionRead, v locatedContent, repo *exchangeContentRepository, _ string) (exchangecontent.Record, error) {
		return repo.Get(s.lease.ctx, id, r.now())
	})
}
func (r *ContentInspectionReader) GetConversationEvidence(ctx context.Context, id string) (exchangecontent.ConversationEvidence, error) {
	return readInspectedContent(r, ctx, id, "", func(s *contentInspectionRead, v locatedContent, repo *exchangeContentRepository, _ string) (exchangecontent.ConversationEvidence, error) {
		return repo.GetConversationEvidence(s.lease.ctx, id, r.now())
	})
}
func readInspectedContentBatch[T any](r *ContentInspectionReader, ctx context.Context, ids []string, read func(*contentInspectionRead, locatedContent) (T, bool, error)) (map[string]T, time.Time, error) {
	if len(ids) > exchangecontent.MaxRequestPreviewBatch {
		return nil, time.Time{}, exchangecontent.ErrInvalidEvidence
	}
	s, err := r.begin(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer s.close()
	result := make(map[string]T, len(ids))
	var expiry time.Time
	for _, id := range uniqueStrings(ids) {
		selected, err := r.selectContent(s, id, nil)
		if errors.Is(err, exchangecontent.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, time.Time{}, err
		}
		value, include, err := read(s, selected)
		if err != nil {
			return nil, time.Time{}, err
		}
		if include {
			result[id] = value
			at := selected.reference.manifest.ExpiresAt
			if expiry.IsZero() || at.Before(expiry) {
				expiry = at
			}
		}
	}
	s.endSQL()
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return nil, time.Time{}, err
	}
	if !expiry.IsZero() && !expiry.After(r.now()) {
		return nil, time.Time{}, ErrInspectionChanged
	}
	return result, expiry, nil
}

func (r *ContentInspectionReader) RequestPreviews(ctx context.Context, ids []string) (map[string]exchangecontent.RequestPreview, error) {
	result, _, err := readInspectedContentBatch(r, ctx, ids, func(s *contentInspectionRead, v locatedContent) (exchangecontent.RequestPreview, bool, error) {
		values, err := r.repository(s, v).RequestPreviews(s.lease.ctx, []string{v.reference.manifest.ExchangeID}, r.now())
		value, ok := values[v.reference.manifest.ExchangeID]
		if ok {
			value.InspectionExpiresAt = v.reference.manifest.ExpiresAt
		}
		return value, ok, err
	})
	return result, err
}
func (r *ContentInspectionReader) InspectAvailableBodies(ctx context.Context, ids []string) (exchangecontent.BodyAvailability, error) {
	result, expiry, err := readInspectedContentBatch(r, ctx, ids, func(s *contentInspectionRead, v locatedContent) (bool, bool, error) {
		return v.reference.manifest.Mode == environment.ContentRecordingFull, true, nil
	})
	if err != nil {
		return exchangecontent.BodyAvailability{}, err
	}
	for _, id := range ids {
		if _, ok := result[id]; !ok {
			result[id] = false
		}
	}
	return exchangecontent.BodyAvailability{Items: result, InspectionExpiresAt: expiry}, nil
}
func (r *ContentInspectionReader) AvailableBodies(ctx context.Context, ids []string) (map[string]bool, error) {
	result, err := r.InspectAvailableBodies(ctx, ids)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

var _ exchangecontent.InspectionReader = (*ContentInspectionReader)(nil)
