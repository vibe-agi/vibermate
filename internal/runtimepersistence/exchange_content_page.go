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
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// Ordinals are resolved from this Exchange's frozen root, never from a digest
// supplied by a caller. A cursor is a position, not a bearer capability.
type contentCursor struct {
	Exchange string `json:"exchange"`
	Root     string `json:"root"`
	Location string `json:"location"`
	Kind     string `json:"kind"`
	Depth    int    `json:"depth"`
	Lower    int    `json:"lower"`
	Block    int    `json:"block"`
	Offset   int    `json:"offset"`
}

func encodeContentCursor(cursor contentCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeContentCursor(encoded string) (contentCursor, error) {
	var cursor contentCursor
	if encoded == "" || len(encoded) > exchangecontent.MaxPageCursorBytes {
		return cursor, exchangecontent.ErrInvalidEvidence
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return cursor, exchangecontent.ErrInvalidEvidence
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || encodeContentCursor(cursor) != encoded ||
		cursor.Depth < 0 || cursor.Lower < 0 || cursor.Block < 0 || cursor.Offset < 0 ||
		!validStoredDigest(cursor.Root) {
		return contentCursor{}, exchangecontent.ErrInvalidEvidence
	}
	return cursor, nil
}

func pageCursor(reference storedContentReference, location, kind string, depth int) contentCursor {
	root := reference.requestRoot
	if location == "response" {
		root = reference.responseDigest.String
	}
	if location == "system" {
		root = reference.systemDigest.String
	}
	if location == "request_evidence" || location == "response_evidence" {
		var encoded []byte
		if location == "request_evidence" {
			encoded, _ = json.Marshal(reference.manifest.Request)
		} else {
			encoded, _ = json.Marshal(reference.manifest.Response)
		}
		digest := sha256.Sum256(encoded)
		root = hex.EncodeToString(digest[:])
	}
	return contentCursor{Exchange: reference.manifest.ExchangeID, Root: root, Location: location, Kind: kind, Depth: depth}
}

func deferredPageBlock(cursor contentCursor, size int) exchangecontent.Block {
	return exchangecontent.Block{Kind: "deferred", Availability: exchangecontent.AvailabilityRecorded,
		Deferred: &exchangecontent.DeferredContent{ExchangeID: cursor.Exchange, Cursor: encodeContentCursor(cursor), EstimatedBytes: size}}
}

func (repository *exchangeContentRepository) GetPagedProjection(ctx context.Context, id string, now time.Time, view exchangecontent.RequestView) (exchangecontent.Projection, error) {
	if view != exchangecontent.RequestViewFull && view != exchangecontent.RequestViewIncremental {
		return exchangecontent.Projection{}, exchangecontent.ErrInvalidEvidence
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return exchangecontent.Projection{}, err
	}
	defer finish()
	ref, err := loadStoredContentReference(operation, repository.reads, id, now)
	if err != nil {
		return exchangecontent.Projection{}, err
	}
	ledger, err := repository.pageReadLedger(ref)
	if err != nil {
		return exchangecontent.Projection{}, err
	}
	paging := &exchangecontent.ProjectionPage{RequestOffset: ref.requestCount}
	var messages []exchangecontent.Message
	if view == exchangecontent.RequestViewFull || ref.inherited > 0 {
		cursor := pageCursor(ref, "request", "request", ref.requestCount)
		if view == exchangecontent.RequestViewIncremental {
			cursor.Lower = ref.inherited
		}
		if cursor.Depth > cursor.Lower {
			page, err := repository.requestContentPage(operation, ref, cursor, ledger)
			if err != nil {
				return exchangecontent.Projection{}, err
			}
			messages, paging.RequestOffset, paging.RequestNextCursor = page.Messages, page.Offset, page.NextCursor
		}
	} else {
		// A checkpoint is history, not new user input. Do not hydrate it simply
		// because its request row became visible.
		paging.RequestNextCursor = encodeContentCursor(pageCursor(ref, "request", "request", ref.requestCount))
	}
	var system []exchangecontent.Block
	if ref.systemDigest.Valid {
		message, _, err := repository.inlinePageMessage(operation, ref.systemDigest.String, pageCursor(ref, "system", "message", 0), exchangecontent.PageContentBytes, ref.manifest.Mode, ledger)
		if err != nil {
			return exchangecontent.Projection{}, err
		}
		if message.Role != "system" && message.Role != "unknown" {
			return exchangecontent.Projection{}, exchangecontent.ErrInvalidEvidence
		}
		system = message.Blocks
	}
	var response *exchangecontent.Message
	if ref.responseDigest.Valid {
		message, _, err := repository.inlinePageMessage(operation, ref.responseDigest.String, pageCursor(ref, "response", "message", 0), exchangecontent.PageContentBytes, ref.manifest.Mode, ledger)
		if err != nil {
			return exchangecontent.Projection{}, err
		}
		if message.Role != "assistant" && message.Role != "unknown" {
			return exchangecontent.Projection{}, exchangecontent.ErrInvalidEvidence
		}
		response = &message
	}
	manifest := ref.manifest
	if len(manifest.Request.ProtocolEvidence) > exchangecontent.PageMessageLimit {
		cursor := pageCursor(ref, "request_evidence", "protocol", 0)
		cursor.Offset = exchangecontent.PageMessageLimit
		paging.RequestEvidenceNextCursor = encodeContentCursor(cursor)
		manifest.Request.ProtocolEvidence = manifest.Request.ProtocolEvidence[:exchangecontent.PageMessageLimit]
	}
	if manifest.Response != nil && len(manifest.Response.ProtocolEvidence) > exchangecontent.PageMessageLimit {
		cursor := pageCursor(ref, "response_evidence", "protocol", 0)
		cursor.Offset = exchangecontent.PageMessageLimit
		paging.ResponseEvidenceNextCursor = encodeContentCursor(cursor)
		metadata := *manifest.Response
		metadata.ProtocolEvidence = metadata.ProtocolEvidence[:exchangecontent.PageMessageLimit]
		manifest.Response = &metadata
	}
	if repository.limits != nil {
		return projectionFromStoredManifestWithin(operation, *repository.limits, manifest, system, messages, response, ref.requestCount, ref.inherited, view, paging)
	}
	return projectionFromStoredManifest(manifest, system, messages, response, ref.requestCount, ref.inherited, view, paging)
}

func contentPageBase(ref storedContentReference, kind string) exchangecontent.ContentPage {
	return exchangecontent.ContentPage{ExchangeID: ref.manifest.ExchangeID, Kind: kind,
		Messages: []exchangecontent.Message{}, Blocks: []exchangecontent.Block{},
		Parent: ref.manifest.Parent, Frozen: ref.manifest.Frozen, Mode: ref.manifest.Mode}
}

func (repository *exchangeContentRepository) GetContentPage(ctx context.Context, id string, now time.Time, encoded string) (exchangecontent.ContentPage, error) {
	cursor, err := decodeContentCursor(encoded)
	if err != nil || cursor.Exchange != id {
		return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
	}
	if repository.limits == nil && cursor.Offset > exchangecontent.MaxEncodedBytes || repository.limits != nil && uint64(cursor.Offset) > repository.limits.RetainedBytes {
		return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return exchangecontent.ContentPage{}, err
	}
	defer finish()
	ref, err := loadStoredContentReference(operation, repository.reads, id, now)
	if err != nil {
		return exchangecontent.ContentPage{}, err
	}
	want := pageCursor(ref, cursor.Location, cursor.Kind, cursor.Depth)
	if cursor.Root != want.Root || !validStoredDigest(want.Root) {
		return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
	}
	ledger, err := repository.pageReadLedger(ref)
	if err != nil {
		return exchangecontent.ContentPage{}, err
	}
	if cursor.Kind == "protocol" {
		if cursor.Depth != 0 || cursor.Lower != 0 || cursor.Block != 0 {
			return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
		}
		values := ref.manifest.Request.ProtocolEvidence
		if cursor.Location == "response_evidence" && ref.manifest.Response != nil {
			values = ref.manifest.Response.ProtocolEvidence
		} else if cursor.Location != "request_evidence" {
			return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
		}
		if cursor.Offset >= len(values) {
			return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
		}
		end := min(len(values), cursor.Offset+exchangecontent.PageMessageLimit)
		page := contentPageBase(ref, "protocol")
		page.Offset, page.Total, page.ProtocolEvidence = cursor.Offset, len(values), values[cursor.Offset:end]
		if end < len(values) {
			cursor.Offset = end
			page.NextCursor = encodeContentCursor(cursor)
		}
		return repository.checkedContentPage(operation, page)
	}
	if cursor.Kind == "request" {
		if cursor.Location != "request" || cursor.Block != 0 || cursor.Offset != 0 ||
			(cursor.Lower != 0 && cursor.Lower != ref.inherited) {
			return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
		}
		return repository.requestContentPage(operation, ref, cursor, ledger)
	}
	if cursor.Lower != 0 || (cursor.Kind != "message" && cursor.Kind != "body") {
		return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
	}
	digest, err := repository.cursorMessage(operation, ref, cursor)
	if err != nil {
		return exchangecontent.ContentPage{}, err
	}
	if repository.limits != nil {
		return repository.sourceMessagePage(operation, ref, cursor, digest, ledger)
	}
	if _, _, err := repository.inlineMessageSize(operation, digest, protocolcore.MaxContentBlocks); err != nil {
		return exchangecontent.ContentPage{}, err
	}
	// ponytail: an explicitly opened large message is verified in full (at
	// most 32 MiB) before returning a bounded fragment. A future authenticated
	// block-manifest format can remove this verification read without weakening
	// the existing canonical message-digest guarantee. Never load other messages.
	resolved, err := loadStoredMessagesByDigest(operation, repository.reads, []string{digest})
	if err != nil {
		return exchangecontent.ContentPage{}, err
	}
	message, ok := resolved[digest]
	if !ok || cursor.Block >= len(message.Blocks) {
		return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
	}
	if cursor.Kind == "body" {
		return contentBodyPage(ref, cursor, message.Blocks[cursor.Block])
	}
	if cursor.Offset != 0 {
		return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
	}
	page := contentPageBase(ref, "message")
	page.Offset, page.Total = cursor.Block, len(message.Blocks)
	budget := exchangecontent.PageContentBytes
	for index := cursor.Block; index < len(message.Blocks); index++ {
		block := message.Blocks[index]
		_, encoded, err := encodeStoredBlock(block)
		if err != nil {
			return exchangecontent.ContentPage{}, err
		}
		if len(page.Blocks) >= exchangecontent.PageMessageLimit || (len(encoded) > budget && len(encoded) <= exchangecontent.PageContentBytes) {
			cursor.Block = index
			page.NextCursor = encodeContentCursor(cursor)
			break
		}
		if len(encoded) > exchangecontent.PageContentBytes {
			body := cursor
			body.Kind, body.Block, body.Offset = "body", index, 0
			block = deferredPageBlock(body, len(encoded))
			budget -= 1024
		} else {
			budget -= len(encoded)
		}
		page.Blocks = append(page.Blocks, block)
	}
	return page, page.Validate()
}

func contentBodyPage(ref storedContentReference, cursor contentCursor, block exchangecontent.Block) (exchangecontent.ContentPage, error) {
	if err := block.Validate(ref.manifest.Mode); err != nil {
		return exchangecontent.ContentPage{}, err
	}
	text, kind := block.Text, "text"
	if len(block.Arguments) != 0 {
		text, kind = string(block.Arguments), "arguments"
	}
	if cursor.Offset >= len(text) || !utf8.ValidString(text) || !utf8.RuneStart(text[cursor.Offset]) {
		return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
	}
	end := min(len(text), cursor.Offset+exchangecontent.PageBodyBytes)
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	page := contentPageBase(ref, kind)
	page.BlockKind, page.CallID, page.ToolName = block.Kind, block.CallID, block.ToolName
	page.Text, page.Offset, page.Total = strings.Clone(text[cursor.Offset:end]), cursor.Offset, len(text)
	if end < len(text) {
		cursor.Offset = end
		page.NextCursor = encodeContentCursor(cursor)
	}
	return page, page.Validate()
}

func (repository *exchangeContentRepository) cursorMessage(ctx context.Context, ref storedContentReference, cursor contentCursor) (string, error) {
	switch cursor.Location {
	case "system":
		if cursor.Depth != 0 || !ref.systemDigest.Valid {
			return "", exchangecontent.ErrInvalidEvidence
		}
		return ref.systemDigest.String, nil
	case "response":
		if cursor.Depth != 0 || !ref.responseDigest.Valid {
			return "", exchangecontent.ErrInvalidEvidence
		}
		return ref.responseDigest.String, nil
	case "request":
		if cursor.Depth < 1 || cursor.Depth > ref.requestCount {
			return "", exchangecontent.ErrInvalidEvidence
		}
		nodes, err := repository.contentNodes(ctx, ref, cursor.Depth, cursor.Depth-1)
		if err != nil || len(nodes) != 1 {
			return "", exchangecontent.ErrInvalidEvidence
		}
		return nodes[0].messageDigest, nil
	default:
		return "", exchangecontent.ErrInvalidEvidence
	}
}

// Only indexed transcript metadata is traversed to validate an ordinal cursor;
// payloads outside the requested window are not read or decompressed.
// ponytail: deep-page seeks walk ancestry metadata; add a verified ordinal index
// if profiling shows these seeks, rather than payload hydration, become hot.
func (repository *exchangeContentRepository) contentNodes(ctx context.Context, ref storedContentReference, before, lower int) ([]storedTranscriptNode, error) {
	if before < 1 || before > ref.requestCount || lower < 0 || lower >= before {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	rows, err := repository.reads.QueryContext(ctx, `WITH RECURSIVE chain(digest,parent_digest,message_digest,depth) AS (
	 SELECT digest,parent_digest,message_digest,depth FROM runtime_exchange_content_transcripts WHERE digest=?
	 UNION ALL SELECT n.digest,n.parent_digest,n.message_digest,n.depth FROM runtime_exchange_content_transcripts n JOIN chain c ON n.digest=c.parent_digest WHERE c.depth>?
	) SELECT digest,parent_digest,message_digest,depth FROM chain`, ref.requestRoot, lower+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storedTranscriptNode
	wantDepth, wantDigest := ref.requestCount, ref.requestRoot
	for rows.Next() {
		var node storedTranscriptNode
		var parent sql.NullString
		if rows.Scan(&node.digest, &parent, &node.messageDigest, &node.depth) != nil {
			return nil, exchangecontent.ErrInvalidEvidence
		}
		if node.depth != wantDepth || node.digest != wantDigest || !validStoredDigest(node.messageDigest) ||
			(node.depth > 1 && (!parent.Valid || !validStoredDigest(parent.String))) || (node.depth == 1 && parent.Valid) ||
			transcriptNodeDigest(parent.String, node.messageDigest) != node.digest {
			return nil, exchangecontent.ErrInvalidEvidence
		}
		if parent.Valid {
			value := parent.String
			node.parentDigest = &value
		}
		if node.depth <= before {
			result = append(result, node)
		}
		wantDepth, wantDigest = node.depth-1, parent.String
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	if wantDepth != lower || len(result) != before-lower {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	return result, nil
}

func (repository *exchangeContentRepository) requestContentPage(ctx context.Context, ref storedContentReference, cursor contentCursor, ledger *storedReadLedger) (exchangecontent.ContentPage, error) {
	page := contentPageBase(ref, "request")
	page.Total = ref.requestCount
	lower := max(cursor.Lower, cursor.Depth-exchangecontent.PageMessageLimit)
	nodes, err := repository.contentNodes(ctx, ref, cursor.Depth, lower)
	if err != nil {
		return exchangecontent.ContentPage{}, err
	}
	budget := exchangecontent.PageContentBytes
	page.Offset = cursor.Depth
	for _, node := range nodes {
		messageCursor := pageCursor(ref, "request", "message", node.depth)
		message, size, err := repository.inlinePageMessage(ctx, node.messageDigest, messageCursor, budget, ref.manifest.Mode, ledger)
		if err != nil {
			return exchangecontent.ContentPage{}, err
		}
		if size > budget && len(page.Messages) > 0 {
			break
		}
		page.Messages = append(page.Messages, message)
		budget -= size
		page.Offset = node.depth - 1
	}
	slices.Reverse(page.Messages)
	if page.Offset > cursor.Lower {
		cursor.Depth = page.Offset
		page.NextCursor = encodeContentCursor(cursor)
	}
	return repository.checkedContentPage(ctx, page)
}

// Sizes are computed from the ordered manifest (including repeated blocks),
// not the deduplicated reference index, and without fetching block payloads.
func (repository *exchangeContentRepository) inlinePageMessage(ctx context.Context, digest string, cursor contentCursor, budget int, mode environment.ContentRecordingMode, ledger *storedReadLedger) (exchangecontent.Message, int, error) {
	if repository.limits != nil {
		probe, err := repository.probeInlineMessage(ctx, digest, exchangecontent.PageMessageLimit, budget)
		if err != nil {
			return exchangecontent.Message{}, 0, err
		}
		if probe.NeedsDeferred {
			if err := ledger.holdShell(); err != nil {
				return exchangecontent.Message{}, 0, err
			}
			estimate := min(probe.InlineUpperBound, repository.limits.CanonicalBytes)
			return exchangecontent.Message{Role: "unknown", Blocks: []exchangecontent.Block{deferredPageBlock(cursor, int(estimate))}}, 1024, nil
		}
		message, err := repository.readVerifiedMessage(ctx, digest, mode, ledger, cursor.Location == "request")
		return message, int(probe.InlineUpperBound), err
	}
	size, count, err := repository.inlineMessageSize(ctx, digest, exchangecontent.PageMessageLimit)
	if err != nil {
		return exchangecontent.Message{}, 0, err
	}
	if size > budget || count > exchangecontent.PageMessageLimit {
		block := deferredPageBlock(cursor, min(size, exchangecontent.MaxEncodedBytes))
		return exchangecontent.Message{Role: "unknown", Blocks: []exchangecontent.Block{block}}, 1024, nil
	}
	resolved, err := loadStoredMessagesByDigest(ctx, repository.reads, []string{digest})
	if err != nil {
		return exchangecontent.Message{}, 0, err
	}
	message, ok := resolved[digest]
	if !ok {
		return exchangecontent.Message{}, 0, errors.New("content page message is missing")
	}
	return message, size, nil
}

func (repository *exchangeContentRepository) inlineMessageSize(ctx context.Context, digest string, blockLimit int) (int, int, error) {
	var count, sum, missing, metadataBytes int
	err := repository.reads.QueryRowContext(ctx, `WITH RECURSIVE message AS (
	 SELECT role,agent_json,block_manifest FROM runtime_exchange_content_messages WHERE digest=?
	 AND length(block_manifest)%64=0 AND length(block_manifest) BETWEEN 64 AND ?
	 AND coalesce(length(agent_json),0)<=4096
	), spans(position) AS (SELECT 1 FROM message UNION ALL SELECT position+64 FROM spans,message WHERE position+64<=min(length(message.block_manifest),?))
	SELECT length(message.block_manifest)/64,coalesce(sum(b.plain_bytes),0),sum(CASE WHEN b.digest IS NULL OR b.plain_bytes<1 OR b.plain_bytes>? THEN 1 ELSE 0 END),
	 coalesce(length(message.agent_json),0)+length(message.role)+64
	FROM message,spans LEFT JOIN runtime_exchange_content_blocks b ON b.digest=substr(message.block_manifest,spans.position,64)`, digest, protocolcore.MaxContentBlocks*64, blockLimit*64, exchangecontent.MaxEncodedBytes).Scan(&count, &sum, &missing, &metadataBytes)
	if err != nil || missing != 0 || count < 1 || sum > exchangecontent.MaxEncodedBytes {
		return 0, 0, exchangecontent.ErrInvalidEvidence
	}
	if count > blockLimit {
		return 0, count, nil // Unknown size; never walk a large manifest for a preview.
	}
	return sum + count + metadataBytes, count, nil
}

func (repository *exchangeContentRepository) pageReadLedger(ref storedContentReference) (*storedReadLedger, error) {
	if repository.limits == nil {
		return nil, nil
	}
	if ref.requestCount > 100001 {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	return newStoredReadLedger(*repository.limits, ref.manifest)
}

func (repository *exchangeContentRepository) checkedContentPage(ctx context.Context, page exchangecontent.ContentPage) (exchangecontent.ContentPage, error) {
	var err error
	if repository.limits != nil {
		err = page.ValidateWithin(ctx, *repository.limits)
	} else {
		err = page.Validate()
	}
	if err != nil {
		return exchangecontent.ContentPage{}, err
	}
	return page, nil
}

type storedInlineProbe struct {
	PhysicalSlots    uint64
	InlineUpperBound uint64
	NeedsDeferred    bool
}

func (repository *exchangeContentRepository) probeInlineMessage(ctx context.Context, digest string, window, budget int) (storedInlineProbe, error) {
	var count, sum, missing, metadata int64
	err := repository.reads.QueryRowContext(ctx, `WITH RECURSIVE message AS (
	 SELECT role,agent_json,block_manifest FROM runtime_exchange_content_messages WHERE digest=?
	 AND length(CAST(block_manifest AS BLOB))%64=0 AND length(CAST(block_manifest AS BLOB)) BETWEEN 64 AND ?
	 AND coalesce(length(agent_json),0)<=4096 AND length(CAST(role AS BLOB))<=32
	), spans(position) AS (SELECT 1 FROM message UNION ALL SELECT position+64 FROM spans,message WHERE position+64<=min(length(message.block_manifest),?))
	SELECT length(CAST(message.block_manifest AS BLOB))/64,coalesce(sum(b.plain_bytes),0),sum(CASE WHEN b.digest IS NULL OR length(CAST(b.digest AS BLOB))!=64 OR b.digest GLOB '*[^0-9a-f]*' OR b.plain_bytes<1 OR b.plain_bytes>? OR length(b.payload) NOT BETWEEN 1 AND ? OR b.codec NOT IN ('identity','zstd') THEN 1 ELSE 0 END),
	 6*(coalesce(length(message.agent_json),0)+length(message.role))+64
	FROM message,spans LEFT JOIN runtime_exchange_content_blocks b ON b.digest=substr(message.block_manifest,spans.position,64)`, digest, storedManifestSlots*storedDigestHexBytes, window*storedDigestHexBytes, exchangecontent.MaxEncodedBytes, exchangecontent.MaxEncodedBytes).Scan(&count, &sum, &missing, &metadata)
	if err != nil || count < 1 || count > storedManifestSlots || sum < 1 || missing != 0 || metadata < 0 {
		return storedInlineProbe{}, exchangecontent.ErrInvalidEvidence
	}
	upper := uint64(sum + metadata + count)
	return storedInlineProbe{PhysicalSlots: uint64(count), InlineUpperBound: upper, NeedsDeferred: count > int64(window) || upper > uint64(max(budget, 0))}, nil
}

func (repository *exchangeContentRepository) sourceMessagePage(ctx context.Context, ref storedContentReference, cursor contentCursor, digest string, ledger *storedReadLedger) (exchangecontent.ContentPage, error) {
	if cursor.Kind == "message" && cursor.Offset != 0 {
		return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
	}
	result, err := repository.readSelectedMessage(ctx, digest, ref.manifest.Mode, ledger, cursor.Location == "request", &storedMessageReadRequest{First: cursor.Block, Body: cursor.Kind == "body", Offset: uint64(cursor.Offset)})
	if err != nil {
		return exchangecontent.ContentPage{}, err
	}
	if cursor.Location == "response" && result.Message.Role != "assistant" || cursor.Location == "system" && result.Message.Role != "system" {
		return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
	}
	if cursor.Kind == "body" {
		body := result.Body
		kind, total := "text", body.Facts.TextBytes
		if body.Arguments {
			kind, total = "arguments", body.Facts.ArgumentBytes
		}
		page := contentPageBase(ref, kind)
		page.BlockKind, page.CallID, page.ToolName = body.Facts.Shape.Kind, body.Facts.Shape.CallID, body.Facts.Shape.ToolName
		// The returned string owns a copy; the selected byte range still lives
		// until this operation returns and is charged independently.
		if uint64(len(body.Bytes)) > ledger.bound-ledger.live {
			return exchangecontent.ContentPage{}, exchangecontent.ErrInvalidEvidence
		}
		ledger.live += uint64(len(body.Bytes))
		page.Text, page.Offset, page.Total = string(body.Bytes), cursor.Offset, int(total)
		if body.End < total {
			cursor.Offset = int(body.End)
			page.NextCursor = encodeContentCursor(cursor)
		}
		return repository.checkedContentPage(ctx, page)
	}
	page := contentPageBase(ref, "message")
	page.Offset, page.Total, page.Blocks = cursor.Block, result.Total, result.Message.Blocks
	for index, size := range result.Deferred {
		body := cursor
		body.Kind = "body"
		body.Block = cursor.Block + index
		body.Offset = 0
		page.Blocks[index] = deferredPageBlock(body, int(size))
	}
	if result.Next >= 0 {
		cursor.Block = result.Next
		page.NextCursor = encodeContentCursor(cursor)
	}
	return repository.checkedContentPage(ctx, page)
}
