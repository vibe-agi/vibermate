package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const transcriptNodeDomain = "vibermate:exchange-transcript-node\x00"

type exchangeContentRepository struct {
	database     *sql.DB
	reads        *sql.DB
	operations   *operationGate
	limits       *exchangecontent.SourceLimits
	loadPhysical storedPhysicalLoader
}

type storedExchangeContentManifest struct {
	ExchangeID string                           `json:"exchangeId"`
	Parent     exchangecontent.ParentRef        `json:"parent"`
	Frozen     exchangecontent.FrozenRef        `json:"frozen"`
	Mode       environment.ContentRecordingMode `json:"mode"`
	RecordedAt time.Time                        `json:"recordedAt"`
	ExpiresAt  time.Time                        `json:"expiresAt"`
	Request    storedRequestManifest            `json:"request"`
	Response   *storedResponseManifest          `json:"response,omitempty"`
}

type storedRequestManifest struct {
	RequestedModel   string                               `json:"requestedModel"`
	EffectiveModel   string                               `json:"effectiveModel"`
	MaxOutputTokens  int                                  `json:"maxOutputTokens"`
	Stream           bool                                 `json:"stream"`
	Tools            []exchangecontent.ToolDefinition     `json:"tools"`
	ProtocolEvidence []protocolcore.ProtocolEvidenceValue `json:"protocolEvidence,omitempty"`
}

type storedResponseManifest struct {
	ID               string                               `json:"id"`
	RequestedModel   string                               `json:"requestedModel"`
	EffectiveModel   string                               `json:"effectiveModel"`
	ReportedModel    string                               `json:"reportedModel"`
	StopReason       string                               `json:"stopReason"`
	Usage            exchangecontent.Usage                `json:"usage"`
	ProtocolEvidence []protocolcore.ProtocolEvidenceValue `json:"protocolEvidence,omitempty"`
	// Empty terminals retain status/usage without adding an invented message
	// to the transcript. Omitted on all pre-existing, non-empty records.
	EmptyOutput bool `json:"emptyOutput,omitempty"`
}

type storedTranscript struct {
	requestRoot    string
	expectedRoot   string
	requestCount   int
	expectedCount  int
	responseDigest string
	// systemDigest addresses the dialect's top-level instruction parameter. It
	// is content-addressed like every message, so identical instructions are
	// stored once, but it is deliberately not a chain node: it is per-request
	// configuration and treating it as history forked the transcript on every
	// turn.
	systemDigest string
	messages     map[string][]byte
	nodes        []storedTranscriptNode
}

type storedTranscriptNode struct {
	digest        string
	parentDigest  *string
	messageDigest string
	depth         int
}

type storedTranscriptCandidate struct {
	Digest string `json:"digest"`
	Depth  int    `json:"depth"`
}

type storedContentReference struct {
	manifest       storedExchangeContentManifest
	requestRoot    string
	expectedRoot   string
	baseRoot       sql.NullString
	responseDigest sql.NullString
	systemDigest   sql.NullString
	requestCount   int
	expectedCount  int
	inherited      int
}

var _ exchangecontent.Repository = (*exchangeContentRepository)(nil)

func newExchangeContentRepository(
	database, reads *sql.DB,
	operations *operationGate,
	limits *exchangecontent.SourceLimits,
) *exchangeContentRepository {
	repository := &exchangeContentRepository{database: database, reads: reads, operations: operations, limits: limits}
	repository.loadPhysical = repository.physicalContentRow
	return repository
}

func (repository *exchangeContentRepository) Put(
	ctx context.Context,
	record exchangecontent.Record,
) error {
	if repository.limits != nil {
		source, err := exchangecontent.SourceFromRecordWithin(*repository.limits, record)
		if err != nil {
			return err
		}
		return repository.PutSource(ctx, source)
	}
	if err := record.Validate(); err != nil {
		return err
	}
	manifest, encodedManifest, transcript, err := encodeStoredContent(record)
	if err != nil {
		return err
	}
	return repository.publishContent(ctx, manifest, encodedManifest, transcript, func(ctx context.Context, tx *sql.Tx, digest string) error {
		payload, ok := transcript.messages[digest]
		if !ok {
			return exchangecontent.ErrInvalidEvidence
		}
		return putStoredMessage(ctx, tx, digest, payload)
	})
}

func (repository *exchangeContentRepository) publishContent(ctx context.Context, manifest storedExchangeContentManifest, encodedManifest []byte, transcript storedTranscript, writeMessage func(context.Context, *sql.Tx, string) error) error {
	scopeKind, scopeID := storedContentScope(manifest.Parent)
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return fmt.Errorf("begin Exchange content transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	completed, err := completeStoredExchangeContent(
		operation,
		transaction,
		manifest,
		&encodedManifest,
		transcript,
		scopeKind,
		scopeID,
		writeMessage,
		repository.limits,
	)
	if err != nil {
		return err
	}
	if completed {
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit Exchange content completion: %w", err)
		}
		return nil
	}
	baseDigest, inherited, err := findStoredTranscriptBase(
		operation,
		transaction,
		scopeKind,
		scopeID,
		transcript.nodes,
		transcript.requestCount,
	)
	if err != nil {
		return err
	}
	if transcript.systemDigest != "" {
		if err := writeMessage(operation, transaction, transcript.systemDigest); err != nil {
			return err
		}
	}
	for index := inherited; index < len(transcript.nodes); index++ {
		node := transcript.nodes[index]
		if err := writeMessage(operation, transaction, node.messageDigest); err != nil {
			return err
		}
		if err := putStoredTranscriptNode(operation, transaction, node); err != nil {
			return err
		}
	}
	var base any
	if baseDigest != "" {
		base = baseDigest
	}
	_, err = transaction.ExecContext(
		operation,
		`INSERT INTO runtime_exchange_contents (
		   exchange_id, scope_kind, scope_id, mode,
		   recorded_at_unix_ms, expires_at_unix_ms,
		   request_transcript_digest, expected_transcript_digest,
		   base_transcript_digest, request_message_count,
		   expected_message_count, inherited_message_count,
		   response_message_digest, system_message_digest, manifest_json
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		manifest.ExchangeID,
		scopeKind,
		scopeID,
		string(manifest.Mode),
		toUnixMillis(manifest.RecordedAt),
		toUnixMillis(manifest.ExpiresAt),
		transcript.requestRoot,
		transcript.expectedRoot,
		base,
		transcript.requestCount,
		transcript.expectedCount,
		inherited,
		nullableDigest(transcript.responseDigest),
		nullableDigest(transcript.systemDigest),
		encodedManifest,
	)
	if err != nil {
		return fmt.Errorf("persist Exchange content manifest: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit Exchange content transaction: %w", err)
	}
	return nil
}

func completeStoredExchangeContent(
	ctx context.Context,
	transaction *sql.Tx,
	manifest storedExchangeContentManifest,
	encodedManifest *[]byte,
	transcript storedTranscript,
	scopeKind, scopeID string,
	writeMessage func(context.Context, *sql.Tx, string) error,
	limits *exchangecontent.SourceLimits,
) (bool, error) {
	var storedScopeKind, storedScopeID, storedMode string
	var recordedMillis, expiresMillis int64
	var requestRoot, expectedRoot string
	var baseRoot, responseDigest, systemDigest sql.NullString
	var requestCount, expectedCount, inherited int
	var storedEncodedManifest []byte
	err := transaction.QueryRowContext(
		ctx,
		`SELECT scope_kind, scope_id, mode,
		        recorded_at_unix_ms, expires_at_unix_ms,
		        request_transcript_digest, expected_transcript_digest,
		        base_transcript_digest, request_message_count,
		        expected_message_count, inherited_message_count,
		        response_message_digest, system_message_digest,
		        CASE WHEN length(manifest_json) BETWEEN 1 AND 33554432 THEN manifest_json END
		 FROM runtime_exchange_contents
		 WHERE exchange_id = ?`,
		manifest.ExchangeID,
	).Scan(
		&storedScopeKind,
		&storedScopeID,
		&storedMode,
		&recordedMillis,
		&expiresMillis,
		&requestRoot,
		&expectedRoot,
		&baseRoot,
		&requestCount,
		&expectedCount,
		&inherited,
		&responseDigest,
		&systemDigest,
		&storedEncodedManifest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load pending Exchange content: %w", err)
	}
	storedManifest, err := decodeStoredContentManifestWithin(ctx, storedEncodedManifest, limits)
	if err != nil {
		return false, err
	}
	addedMessages := 1
	if manifest.Response != nil && manifest.Response.EmptyOutput {
		addedMessages = 0
	}
	if err != nil || storedManifest.ExchangeID != manifest.ExchangeID ||
		storedManifest.Response != nil || manifest.Response == nil ||
		storedScopeKind != scopeKind || storedScopeID != scopeID ||
		storedMode != string(manifest.Mode) ||
		toUnixMillis(storedManifest.RecordedAt) != recordedMillis ||
		toUnixMillis(storedManifest.ExpiresAt) != expiresMillis ||
		!reflect.DeepEqual(storedManifest.Parent, manifest.Parent) ||
		!reflect.DeepEqual(storedManifest.Frozen, manifest.Frozen) ||
		!reflect.DeepEqual(storedManifest.Request, manifest.Request) ||
		requestRoot != transcript.requestRoot || expectedRoot != requestRoot ||
		requestCount != transcript.requestCount || expectedCount != requestCount ||
		responseDigest.Valid ||
		systemDigest.String != transcript.systemDigest ||
		inherited < 0 || inherited > requestCount ||
		(transcript.responseDigest == "") != manifest.Response.EmptyOutput ||
		len(transcript.nodes) != transcript.expectedCount ||
		transcript.expectedCount != requestCount+addedMessages {
		return false, exchangecontent.ErrInvalidEvidence
	}
	if inherited == 0 && baseRoot.Valid || inherited > 0 && !baseRoot.Valid {
		return false, exchangecontent.ErrInvalidEvidence
	}
	manifest.RecordedAt = storedManifest.RecordedAt
	manifest.ExpiresAt = storedManifest.ExpiresAt
	encoded, err := json.Marshal(manifest)
	if err != nil || len(encoded) > exchangecontent.MaxEncodedBytes {
		return false, exchangecontent.ErrInvalidEvidence
	}
	*encodedManifest = encoded
	if !manifest.Response.EmptyOutput {
		responseNode := transcript.nodes[len(transcript.nodes)-1]
		if responseNode.digest != transcript.expectedRoot {
			return false, exchangecontent.ErrInvalidEvidence
		}
		if err := writeMessage(ctx, transaction, responseNode.messageDigest); err != nil {
			return false, err
		}
		if err := putStoredTranscriptNode(ctx, transaction, responseNode); err != nil {
			return false, err
		}
	}
	result, err := transaction.ExecContext(
		ctx,
		`UPDATE runtime_exchange_contents
		 SET expected_transcript_digest = ?,
		     expected_message_count = ?,
		     response_message_digest = ?,
		     manifest_json = ?
		 WHERE exchange_id = ? AND response_message_digest IS NULL`,
		transcript.expectedRoot,
		transcript.expectedCount,
		nullableDigest(transcript.responseDigest),
		*encodedManifest,
		manifest.ExchangeID,
	)
	if err != nil {
		return false, fmt.Errorf("complete Exchange content manifest: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return false, exchangecontent.ErrInvalidEvidence
	}
	return true, nil
}

func (repository *exchangeContentRepository) Get(
	ctx context.Context,
	exchangeID string,
	now time.Time,
) (exchangecontent.Record, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return exchangecontent.Record{}, err
	}
	defer finish()
	reference, err := loadStoredContentReferenceWithin(
		operation, repository.reads, exchangeID, now, repository.limits,
	)
	if err != nil {
		return exchangecontent.Record{}, err
	}
	if repository.limits != nil {
		p, err := repository.readSourceProjection(operation, reference, exchangecontent.RequestViewFull)
		if err != nil {
			return exchangecontent.Record{}, err
		}
		return exchangecontent.Record{ExchangeID: p.ExchangeID, Parent: p.Parent, Frozen: p.Frozen, Mode: p.Mode, RecordedAt: p.RecordedAt, ExpiresAt: p.ExpiresAt, Request: p.Request, Response: p.Response, Presentation: p.Presentation}, nil
	}
	return loadFullStoredContent(operation, repository.reads, reference)
}

func (repository *exchangeContentRepository) GetProjection(
	ctx context.Context,
	exchangeID string,
	now time.Time,
	view exchangecontent.RequestView,
) (exchangecontent.Projection, error) {
	if view != exchangecontent.RequestViewFull &&
		view != exchangecontent.RequestViewIncremental {
		return exchangecontent.Projection{}, exchangecontent.ErrInvalidEvidence
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return exchangecontent.Projection{}, err
	}
	defer finish()
	reference, err := loadStoredContentReferenceWithin(
		operation, repository.reads, exchangeID, now, repository.limits,
	)
	if err != nil {
		return exchangecontent.Projection{}, err
	}
	if repository.limits != nil {
		p, err := repository.readSourceProjection(operation, reference, view)
		if err != nil || view != exchangecontent.RequestViewFull {
			return p, err
		}
		// This is the complete verified Record, not a suffix dressed as one.
		// ProjectWithin preserves legacy array/presentation semantics. Its raw
		// clone fits the 2*payload allowance (the parser charges raw only once),
		// cells fit 4*structure, and its bounded map shares the per-Block 4096
		// allowance with tiny builders (both together remain below that bound).
		r := exchangecontent.Record{ExchangeID: p.ExchangeID, Parent: p.Parent, Frozen: p.Frozen, Mode: p.Mode, RecordedAt: p.RecordedAt, ExpiresAt: p.ExpiresAt, Request: p.Request, Response: p.Response, Presentation: p.Presentation}
		return exchangecontent.ProjectWithin(operation, *repository.limits, r, view)
	}
	if view == exchangecontent.RequestViewFull {
		record, loadErr := loadFullStoredContent(operation, repository.reads, reference)
		if loadErr != nil {
			return exchangecontent.Projection{}, loadErr
		}
		return exchangecontent.Project(record, view)
	}

	messages, err := loadStoredTranscriptSuffix(
		operation,
		repository.reads,
		reference.requestRoot,
		reference.baseRoot,
		reference.inherited,
		reference.requestCount,
	)
	if err != nil {
		return exchangecontent.Projection{}, err
	}
	responseMessage, err := loadStoredResponseMessage(
		operation, repository.reads, reference.responseDigest,
	)
	if err != nil {
		return exchangecontent.Projection{}, err
	}
	systemBlocks, err := loadStoredSystemBlocks(
		operation, repository.reads, reference.systemDigest,
	)
	if err != nil {
		return exchangecontent.Projection{}, err
	}
	projection, err := projectionFromStoredManifest(
		reference.manifest,
		systemBlocks,
		messages,
		responseMessage,
		reference.requestCount,
		reference.inherited,
		exchangecontent.RequestViewIncremental,
		nil,
	)
	if err != nil {
		return exchangecontent.Projection{}, err
	}
	return projection.Clone(), nil
}

func newStoredReadLedger(l exchangecontent.SourceLimits, m storedExchangeContentManifest) (*storedReadLedger, error) {
	bound, err := storedMaterializationBound(l)
	if err != nil {
		return nil, err
	}
	ledger := &storedReadLedger{limits: l, bound: bound}
	stringsCost := func(structure uint64, values ...string) error {
		var n uint64
		for _, v := range values {
			if uint64(len(v)) > l.RetainedBytes-n {
				return exchangecontent.ErrInvalidEvidence
			}
			n += uint64(len(v))
		}
		return ledger.parent(n, structure)
	}
	f := m.Frozen
	if err := stringsCost(uint64(reflect.TypeFor[exchangecontent.Record]().Size()), m.ExchangeID, m.Parent.CaptureRunID, m.Parent.ManualCaptureID, f.EnvironmentID, f.EnvironmentDigest, f.ClientEndpointID, f.ProtocolPlanID, f.RouteID, string(m.Mode), m.Request.RequestedModel, m.Request.EffectiveModel); err != nil {
		return nil, err
	}
	for _, t := range m.Request.Tools {
		if err := stringsCost(uint64(reflect.TypeFor[exchangecontent.ToolDefinition]().Size()), t.Name, t.Namespace); err != nil {
			return nil, err
		}
	}
	evidence := func(values []protocolcore.ProtocolEvidenceValue) error {
		for _, v := range values {
			if err := stringsCost(uint64(reflect.TypeFor[protocolcore.ProtocolEvidenceValue]().Size()), v.Name, v.Value); err != nil {
				return err
			}
		}
		return nil
	}
	if err := evidence(m.Request.ProtocolEvidence); err != nil {
		return nil, err
	}
	if v := m.Response; v != nil {
		if err := stringsCost(uint64(reflect.TypeFor[exchangecontent.Response]().Size()), v.ID, v.RequestedModel, v.EffectiveModel, v.ReportedModel, v.StopReason); err != nil {
			return nil, err
		}
		for _, u := range []exchangecontent.UsageValue{v.Usage.InputUncached, v.Usage.CacheWrite, v.Usage.CacheRead, v.Usage.Output, v.Usage.Reasoning} {
			if err := stringsCost(0, u.Source); err != nil {
				return nil, err
			}
		}
		if err := evidence(v.ProtocolEvidence); err != nil {
			return nil, err
		}
	}
	return ledger, nil
}

func (repository *exchangeContentRepository) readSourceProjection(ctx context.Context, ref storedContentReference, view exchangecontent.RequestView) (exchangecontent.Projection, error) {
	ledger, err := newStoredReadLedger(*repository.limits, ref.manifest)
	if err != nil {
		return exchangecontent.Projection{}, err
	}
	if ref.requestCount > 100001 {
		return exchangecontent.Projection{}, exchangecontent.ErrInvalidEvidence
	}
	lower := 0
	if view == exchangecontent.RequestViewIncremental {
		lower = ref.inherited
	}
	var nodes []storedTranscriptNode
	if lower < ref.requestCount {
		nodes, err = repository.contentNodes(ctx, ref, ref.requestCount, lower)
		if err != nil {
			return exchangecontent.Projection{}, err
		}
		slices.Reverse(nodes)
		if lower > 0 && (nodes[0].parentDigest == nil || *nodes[0].parentDigest != ref.baseRoot.String) {
			return exchangecontent.Projection{}, exchangecontent.ErrInvalidEvidence
		}
	} else if ref.baseRoot.String != ref.requestRoot {
		return exchangecontent.Projection{}, exchangecontent.ErrInvalidEvidence
	}
	var messages []exchangecontent.Message
	for start := 0; start < len(nodes); start += exchangecontent.PageMessageLimit {
		batch := nodes[start:min(len(nodes), start+exchangecontent.PageMessageLimit)]
		digests := make([]string, len(batch))
		for i, node := range batch {
			digests[i] = node.messageDigest
		}
		metadata, err := repository.contentMessageMetadata(ctx, digests)
		if err != nil {
			return exchangecontent.Projection{}, err
		}
		for _, node := range batch {
			result, err := repository.readMessageMetadata(ctx, node.messageDigest, metadata[node.messageDigest], ref.manifest.Mode, ledger, true, nil)
			if err != nil {
				return exchangecontent.Projection{}, err
			}
			messages = append(messages, result.Message)
		}
	}
	if lower == 0 && ref.inherited > 0 && nodes[ref.inherited-1].digest != ref.baseRoot.String {
		return exchangecontent.Projection{}, exchangecontent.ErrInvalidEvidence
	}
	var system []exchangecontent.Block
	if ref.systemDigest.Valid {
		message, err := repository.readVerifiedMessage(ctx, ref.systemDigest.String, ref.manifest.Mode, ledger, false)
		if err != nil {
			return exchangecontent.Projection{}, err
		}
		if message.Role != "system" {
			return exchangecontent.Projection{}, exchangecontent.ErrInvalidEvidence
		}
		system = message.Blocks
	}
	var response *exchangecontent.Message
	if ref.responseDigest.Valid {
		message, err := repository.readVerifiedMessage(ctx, ref.responseDigest.String, ref.manifest.Mode, ledger, false)
		if err != nil {
			return exchangecontent.Projection{}, err
		}
		if message.Role != "assistant" {
			return exchangecontent.Projection{}, exchangecontent.ErrInvalidEvidence
		}
		response = &message
	}
	return projectionFromStoredManifestWithin(ctx, *repository.limits, ref.manifest, system, messages, response, ref.requestCount, ref.inherited, view, nil)
}

func (repository *exchangeContentRepository) AvailableBodies(ctx context.Context, ids []string, now time.Time) (map[string]bool, error) {
	if len(ids) > exchangecontent.MaxRequestPreviewBatch {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	rows, err := repository.reads.QueryContext(operation, `SELECT exchange_id FROM runtime_exchange_contents JOIN json_each(?) AS wanted ON wanted.value=exchange_id WHERE mode='full' AND expires_at_unix_ms>?`, string(encoded), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]bool, len(ids))
	for _, id := range ids {
		result[id] = false
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

// RequestPreviews resolves the final request message for a bounded Activity
// page using bounded metadata and payload reads. It verifies the terminal
// node and the rebuilt message digest; full-chain verification remains the
// responsibility of Get/GetProjection when an operator opens the evidence.
func (repository *exchangeContentRepository) RequestPreviews(
	ctx context.Context,
	exchangeIDs []string,
	now time.Time,
) (map[string]exchangecontent.RequestPreview, error) {
	if len(exchangeIDs) > exchangecontent.MaxRequestPreviewBatch {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	wantedIDs := uniqueStrings(exchangeIDs)
	if len(wantedIDs) == 0 {
		return map[string]exchangecontent.RequestPreview{}, nil
	}
	encodedWanted, err := json.Marshal(wantedIDs)
	if err != nil {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()

	type terminalMessage struct {
		messageDigest string
		mode          environment.ContentRecordingMode
	}
	terminals := make(map[string]terminalMessage, len(wantedIDs))
	digests := make([]string, 0, len(wantedIDs))
	rows, err := repository.reads.QueryContext(
		operation,
		`SELECT contents.exchange_id,
		        contents.request_transcript_digest,
		        contents.request_message_count,
		        nodes.parent_digest,
		        nodes.message_digest,
		        nodes.depth, contents.mode
		   FROM runtime_exchange_contents AS contents
		   JOIN json_each(?) AS wanted ON wanted.value = contents.exchange_id
		   LEFT JOIN runtime_exchange_content_transcripts AS nodes
		     ON nodes.digest = contents.request_transcript_digest
		  WHERE contents.expires_at_unix_ms > ?`,
		string(encodedWanted),
		toUnixMillis(now.UTC()),
	)
	if err != nil {
		return nil, fmt.Errorf("load Exchange request preview roots: %w", err)
	}
	for rows.Next() {
		var exchangeID, root string
		var count int
		var parent, messageDigest sql.NullString
		var depth sql.NullInt64
		var mode environment.ContentRecordingMode
		if err := rows.Scan(
			&exchangeID, &root, &count, &parent, &messageDigest, &depth, &mode,
		); err != nil {
			_ = rows.Close()
			return nil, exchangecontent.ErrInvalidEvidence
		}
		parentValue := ""
		if parent.Valid {
			parentValue = parent.String
		}
		if count < 1 || !validStoredDigest(root) ||
			!messageDigest.Valid || !validStoredDigest(messageDigest.String) ||
			!depth.Valid || depth.Int64 != int64(count) ||
			(count == 1 && parent.Valid) ||
			(count > 1 && (!parent.Valid || !validStoredDigest(parent.String))) ||
			transcriptNodeDigest(parentValue, messageDigest.String) != root {
			_ = rows.Close()
			return nil, exchangecontent.ErrInvalidEvidence
		}
		if _, duplicate := terminals[exchangeID]; duplicate {
			_ = rows.Close()
			return nil, exchangecontent.ErrInvalidEvidence
		}
		terminals[exchangeID] = terminalMessage{
			messageDigest: messageDigest.String,
			mode:          mode,
		}
		digests = append(digests, messageDigest.String)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate Exchange request preview roots: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close Exchange request preview roots: %w", err)
	}

	// Snippets are optional: never inflate a multi-MiB tool return just to
	// render a directory row. Full content stays available through paging.
	inlineDigests := make([]string, 0, len(digests))
	for _, digest := range uniqueStrings(digests) {
		if repository.limits != nil {
			probe, err := repository.probeInlineMessage(operation, digest, exchangecontent.PageMessageLimit, 16<<10)
			if err != nil {
				return nil, err
			}
			if !probe.NeedsDeferred {
				inlineDigests = append(inlineDigests, digest)
			}
			continue
		}
		size, count, err := repository.inlineMessageSize(operation, digest, exchangecontent.PageMessageLimit)
		if err != nil {
			return nil, err
		}
		if size <= 16<<10 && count <= exchangecontent.PageMessageLimit {
			inlineDigests = append(inlineDigests, digest)
		}
	}
	var messages map[string]exchangecontent.Message
	if repository.limits == nil {
		messages, err = loadStoredMessagesByDigest(operation, repository.reads, inlineDigests)
		if err != nil {
			return nil, err
		}
	} else {
		bound, err := storedMaterializationBound(*repository.limits)
		if err != nil {
			return nil, err
		}
		ledger := &storedReadLedger{limits: *repository.limits, bound: bound}
		modes := make(map[string]environment.ContentRecordingMode, len(terminals))
		for _, v := range terminals {
			modes[v.messageDigest] = v.mode
		}
		messages = make(map[string]exchangecontent.Message, len(inlineDigests))
		for _, digest := range inlineDigests {
			// Each preview comes from a separate Source. Logical admission is
			// per Source; earlier preview outputs retain their capacity charges.
			ledger.logical = storedBlockLogicalCost{}
			message, err := repository.readVerifiedMessage(operation, digest, modes[digest], ledger, true)
			if err != nil {
				return nil, err
			}
			messages[digest] = message
		}
	}
	previews := make(map[string]exchangecontent.RequestPreview, len(terminals))
	for exchangeID, terminal := range terminals {
		message, exists := messages[terminal.messageDigest]
		if !exists {
			continue
		}
		if preview, ok := exchangecontent.PreviewRequestMessage(message); ok {
			previews[exchangeID] = preview
		}
	}
	return previews, nil
}

func loadStoredContentReference(
	ctx context.Context,
	database *sql.DB,
	exchangeID string,
	now time.Time,
) (storedContentReference, error) {
	return loadStoredContentReferenceWithin(ctx, database, exchangeID, now, nil)
}

func loadStoredContentReferenceWithin(ctx context.Context, database *sql.DB, exchangeID string, now time.Time, limits *exchangecontent.SourceLimits) (storedContentReference, error) {
	var reference storedContentReference
	var scopeKind, scopeID, mode string
	var recordedMillis, expiresMillis int64
	var encodedManifest []byte
	err := database.QueryRowContext(
		ctx,
		`SELECT scope_kind, scope_id, mode,
		        recorded_at_unix_ms, expires_at_unix_ms,
		        request_transcript_digest, expected_transcript_digest,
		        base_transcript_digest, request_message_count,
		        expected_message_count, inherited_message_count,
		        response_message_digest, system_message_digest,
		        CASE WHEN length(manifest_json) BETWEEN 1 AND 33554432 THEN manifest_json END
		 FROM runtime_exchange_contents
		 WHERE exchange_id = ? AND expires_at_unix_ms > ?`,
		exchangeID,
		toUnixMillis(now.UTC()),
	).Scan(
		&scopeKind,
		&scopeID,
		&mode,
		&recordedMillis,
		&expiresMillis,
		&reference.requestRoot,
		&reference.expectedRoot,
		&reference.baseRoot,
		&reference.requestCount,
		&reference.expectedCount,
		&reference.inherited,
		&reference.responseDigest,
		&reference.systemDigest,
		&encodedManifest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedContentReference{}, exchangecontent.ErrNotFound
	}
	if err != nil {
		return storedContentReference{}, fmt.Errorf("load Exchange content manifest: %w", err)
	}
	manifest, err := decodeStoredContentManifestWithin(ctx, encodedManifest, limits)
	if err != nil {
		return storedContentReference{}, err
	}
	if err != nil || manifest.ExchangeID != exchangeID || string(manifest.Mode) != mode ||
		toUnixMillis(manifest.RecordedAt) != recordedMillis ||
		toUnixMillis(manifest.ExpiresAt) != expiresMillis {
		return storedContentReference{}, exchangecontent.ErrInvalidEvidence
	}
	wantScopeKind, wantScopeID := storedContentScope(manifest.Parent)
	if scopeKind != wantScopeKind || scopeID != wantScopeID ||
		reference.requestCount < 1 || reference.inherited < 0 ||
		reference.inherited > reference.requestCount ||
		!validStoredDigest(reference.requestRoot) ||
		!validStoredDigest(reference.expectedRoot) ||
		(reference.inherited == 0 && reference.baseRoot.Valid) ||
		(reference.inherited > 0 && (!reference.baseRoot.Valid ||
			!validStoredDigest(reference.baseRoot.String))) {
		return storedContentReference{}, exchangecontent.ErrInvalidEvidence
	}
	if manifest.Response == nil || manifest.Response.EmptyOutput {
		if reference.responseDigest.Valid ||
			reference.expectedCount != reference.requestCount ||
			reference.expectedRoot != reference.requestRoot {
			return storedContentReference{}, exchangecontent.ErrInvalidEvidence
		}
	} else {
		if !reference.responseDigest.Valid ||
			!validStoredDigest(reference.responseDigest.String) ||
			reference.expectedCount != reference.requestCount+1 ||
			reference.expectedRoot != transcriptNodeDigest(
				reference.requestRoot, reference.responseDigest.String,
			) {
			return storedContentReference{}, exchangecontent.ErrInvalidEvidence
		}
	}
	reference.manifest = manifest
	return reference, nil
}

func loadFullStoredContent(
	ctx context.Context,
	database *sql.DB,
	reference storedContentReference,
) (exchangecontent.Record, error) {
	messages, err := loadStoredTranscript(
		ctx,
		database,
		reference.requestRoot,
		reference.requestCount,
	)
	if err != nil {
		return exchangecontent.Record{}, err
	}
	responseMessage, err := loadStoredResponseMessage(ctx, database, reference.responseDigest)
	if err != nil {
		return exchangecontent.Record{}, err
	}
	systemBlocks, err := loadStoredSystemBlocks(
		ctx, database, reference.systemDigest,
	)
	if err != nil {
		return exchangecontent.Record{}, err
	}
	record, err := recordFromStoredManifest(
		reference.manifest, systemBlocks, messages, responseMessage,
	)
	if err != nil {
		return exchangecontent.Record{}, err
	}
	// systemDigest is compared for the same reason the roots are: without it, a
	// row pointing at another Exchange's instruction parameter reads back as
	// frozen evidence, because loadStoredSystemBlocks only proves the message
	// hashes to the digest it was fetched by. The write side already makes this
	// comparison in completeStoredExchangeContent.
	rebuilt, err := buildStoredTranscript(record)
	if err != nil || rebuilt.requestRoot != reference.requestRoot ||
		rebuilt.expectedRoot != reference.expectedRoot ||
		rebuilt.requestCount != reference.requestCount ||
		rebuilt.expectedCount != reference.expectedCount ||
		rebuilt.responseDigest != reference.responseDigest.String ||
		rebuilt.systemDigest != reference.systemDigest.String {
		return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
	}
	if reference.inherited == 0 {
		record.Presentation = exchangecontent.RequestPresentation{
			Mode: exchangecontent.RequestPresentationCheckpoint,
		}
	} else {
		if reference.inherited > len(rebuilt.nodes) ||
			rebuilt.nodes[reference.inherited-1].digest != reference.baseRoot.String {
			return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
		}
		presentationMode := exchangecontent.RequestPresentationIncremental
		if reference.inherited == reference.requestCount {
			presentationMode = exchangecontent.RequestPresentationSameTranscript
		}
		record.Presentation = exchangecontent.RequestPresentation{
			Mode: presentationMode, InheritedMessageCount: reference.inherited,
		}
	}
	return record.Clone(), nil
}

func (repository *exchangeContentRepository) PurgeExpired(
	ctx context.Context,
	now time.Time,
) (uint64, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return 0, fmt.Errorf("begin Exchange content purge: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	count, err := purgeExpiredExchangeContent(
		operation, transaction, toUnixMillis(now.UTC()), -1,
	)
	if err != nil {
		return 0, err
	}
	if err := transaction.Commit(); err != nil {
		return 0, fmt.Errorf("commit Exchange content purge: %w", err)
	}
	return uint64(count), nil
}

func encodeStoredContent(record exchangecontent.Record) (
	storedExchangeContentManifest,
	[]byte,
	storedTranscript,
	error,
) {
	manifest := storedExchangeContentManifest{
		ExchangeID: record.ExchangeID,
		Parent:     record.Parent,
		Frozen:     record.Frozen,
		Mode:       record.Mode,
		RecordedAt: record.RecordedAt,
		ExpiresAt:  record.ExpiresAt,
		Request: storedRequestManifest{
			RequestedModel:  record.Request.RequestedModel,
			EffectiveModel:  record.Request.EffectiveModel,
			MaxOutputTokens: record.Request.MaxOutputTokens,
			Stream:          record.Request.Stream,
			Tools:           append([]exchangecontent.ToolDefinition(nil), record.Request.Tools...),
			ProtocolEvidence: append(
				[]protocolcore.ProtocolEvidenceValue(nil),
				record.Request.ProtocolEvidence...,
			),
		},
	}
	if record.Response != nil {
		manifest.Response = &storedResponseManifest{
			EmptyOutput:    len(record.Response.Blocks) == 0,
			ID:             record.Response.ID,
			RequestedModel: record.Response.RequestedModel,
			EffectiveModel: record.Response.EffectiveModel,
			ReportedModel:  record.Response.ReportedModel,
			StopReason:     record.Response.StopReason,
			Usage:          record.Response.Usage,
			ProtocolEvidence: append(
				[]protocolcore.ProtocolEvidenceValue(nil),
				record.Response.ProtocolEvidence...,
			),
		}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || len(encoded) > exchangecontent.MaxEncodedBytes {
		return storedExchangeContentManifest{}, nil, storedTranscript{}, exchangecontent.ErrInvalidEvidence
	}
	transcript, err := buildStoredTranscript(record)
	if err != nil {
		return storedExchangeContentManifest{}, nil, storedTranscript{}, err
	}
	return manifest, encoded, transcript, nil
}

func decodeStoredContentManifest(encoded []byte) (storedExchangeContentManifest, error) {
	return decodeStoredContentManifestWithin(context.Background(), encoded, nil)
}

func recordFromStoredManifest(
	manifest storedExchangeContentManifest,
	systemBlocks []exchangecontent.Block,
	messages []exchangecontent.Message,
	responseMessage *exchangecontent.Message,
) (exchangecontent.Record, error) {
	record := exchangecontent.Record{
		ExchangeID: manifest.ExchangeID,
		Parent:     manifest.Parent,
		Frozen:     manifest.Frozen,
		Mode:       manifest.Mode,
		RecordedAt: manifest.RecordedAt,
		ExpiresAt:  manifest.ExpiresAt,
		Request: exchangecontent.Request{
			RequestedModel:  manifest.Request.RequestedModel,
			EffectiveModel:  manifest.Request.EffectiveModel,
			MaxOutputTokens: manifest.Request.MaxOutputTokens,
			Stream:          manifest.Request.Stream,
			System:          systemBlocks,
			Messages:        messages,
			Tools:           append([]exchangecontent.ToolDefinition(nil), manifest.Request.Tools...),
			ProtocolEvidence: append(
				[]protocolcore.ProtocolEvidenceValue(nil),
				manifest.Request.ProtocolEvidence...,
			),
		},
	}
	if manifest.Response != nil {
		if (responseMessage == nil) != manifest.Response.EmptyOutput {
			return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
		}
		var blocks []exchangecontent.Block
		if responseMessage != nil {
			blocks = responseMessage.Blocks
		}
		record.Response = &exchangecontent.Response{
			ID:             manifest.Response.ID,
			RequestedModel: manifest.Response.RequestedModel,
			EffectiveModel: manifest.Response.EffectiveModel,
			ReportedModel:  manifest.Response.ReportedModel,
			StopReason:     manifest.Response.StopReason,
			Blocks:         cloneStoredBlocks(blocks),
			Usage:          manifest.Response.Usage,
			ProtocolEvidence: append(
				[]protocolcore.ProtocolEvidenceValue(nil),
				manifest.Response.ProtocolEvidence...,
			),
		}
	} else if responseMessage != nil {
		return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
	}
	if err := record.Validate(); err != nil {
		return exchangecontent.Record{}, err
	}
	return record, nil
}

func projectionFromStoredManifest(manifest storedExchangeContentManifest, system []exchangecontent.Block, messages []exchangecontent.Message, response *exchangecontent.Message, total, inherited int, view exchangecontent.RequestView, page *exchangecontent.ProjectionPage) (exchangecontent.Projection, error) {
	p, err := assembleStoredProjection(manifest, system, messages, response, total, inherited, view, page)
	if err != nil {
		return exchangecontent.Projection{}, err
	}
	if err := p.Validate(); err != nil {
		return exchangecontent.Projection{}, err
	}
	return p, nil
}

func projectionFromStoredManifestWithin(ctx context.Context, l exchangecontent.SourceLimits, manifest storedExchangeContentManifest, system []exchangecontent.Block, messages []exchangecontent.Message, response *exchangecontent.Message, total, inherited int, view exchangecontent.RequestView, page *exchangecontent.ProjectionPage) (exchangecontent.Projection, error) {
	p, err := assembleStoredProjection(manifest, system, messages, response, total, inherited, view, page)
	if err != nil {
		return exchangecontent.Projection{}, err
	}
	if err := p.ValidateWithin(ctx, l); err != nil {
		return exchangecontent.Projection{}, err
	}
	return p, nil
}

func assembleStoredProjection(
	manifest storedExchangeContentManifest,
	systemBlocks []exchangecontent.Block,
	messages []exchangecontent.Message,
	responseMessage *exchangecontent.Message,
	totalMessageCount int,
	inherited int,
	view exchangecontent.RequestView,
	page *exchangecontent.ProjectionPage,
) (exchangecontent.Projection, error) {
	presentationMode := exchangecontent.RequestPresentationCheckpoint
	if inherited > 0 {
		presentationMode = exchangecontent.RequestPresentationIncremental
		if inherited == totalMessageCount {
			presentationMode = exchangecontent.RequestPresentationSameTranscript
		}
	}
	projection := exchangecontent.Projection{
		Page:       page,
		ExchangeID: manifest.ExchangeID,
		Parent:     manifest.Parent,
		Frozen:     manifest.Frozen,
		Mode:       manifest.Mode,
		RecordedAt: manifest.RecordedAt,
		ExpiresAt:  manifest.ExpiresAt,
		Request: exchangecontent.Request{
			RequestedModel:  manifest.Request.RequestedModel,
			EffectiveModel:  manifest.Request.EffectiveModel,
			MaxOutputTokens: manifest.Request.MaxOutputTokens,
			Stream:          manifest.Request.Stream,
			System:          systemBlocks,
			Messages:        messages,
			Tools:           append([]exchangecontent.ToolDefinition(nil), manifest.Request.Tools...),
			ProtocolEvidence: append(
				[]protocolcore.ProtocolEvidenceValue(nil),
				manifest.Request.ProtocolEvidence...,
			),
		},
		Presentation: exchangecontent.RequestPresentation{
			Mode:                  presentationMode,
			InheritedMessageCount: inherited,
		},
		View:              view,
		TotalMessageCount: totalMessageCount,
	}
	if manifest.Response != nil {
		if (responseMessage == nil) != manifest.Response.EmptyOutput {
			return exchangecontent.Projection{}, exchangecontent.ErrInvalidEvidence
		}
		var blocks []exchangecontent.Block
		if responseMessage != nil {
			blocks = responseMessage.Blocks
		}
		projection.Response = &exchangecontent.Response{
			ID:             manifest.Response.ID,
			RequestedModel: manifest.Response.RequestedModel,
			EffectiveModel: manifest.Response.EffectiveModel,
			ReportedModel:  manifest.Response.ReportedModel,
			StopReason:     manifest.Response.StopReason,
			Blocks:         blocks,
			Usage:          manifest.Response.Usage,
			ProtocolEvidence: append(
				[]protocolcore.ProtocolEvidenceValue(nil),
				manifest.Response.ProtocolEvidence...,
			),
		}
	} else if responseMessage != nil {
		return exchangecontent.Projection{}, exchangecontent.ErrInvalidEvidence
	}
	return projection, nil
}

func buildStoredTranscript(record exchangecontent.Record) (storedTranscript, error) {
	result := storedTranscript{messages: make(map[string][]byte)}
	if len(record.Request.System) > 0 {
		message := exchangecontent.Message{
			Role: "system", Blocks: cloneStoredBlocks(record.Request.System),
		}
		digest, encoded, err := encodeStoredMessage(message)
		if err != nil {
			return storedTranscript{}, err
		}
		result.messages[digest] = encoded
		result.systemDigest = digest
	}
	parent := ""
	for _, message := range record.Request.Messages {
		digest, encoded, err := encodeStoredMessage(message)
		if err != nil {
			return storedTranscript{}, err
		}
		result.messages[digest] = encoded
		parent = result.appendNode(parent, digest)
	}
	result.requestRoot = parent
	result.requestCount = len(record.Request.Messages)
	result.expectedRoot = parent
	result.expectedCount = result.requestCount
	if record.Response != nil && len(record.Response.Blocks) != 0 {
		message := exchangecontent.Message{
			Role: "assistant", Blocks: cloneStoredBlocks(record.Response.Blocks),
		}
		digest, encoded, err := encodeStoredMessage(message)
		if err != nil {
			return storedTranscript{}, err
		}
		result.messages[digest] = encoded
		result.responseDigest = digest
		result.expectedRoot = result.appendNode(parent, digest)
		result.expectedCount++
	}
	return result, nil
}

func (transcript *storedTranscript) appendNode(parent, messageDigest string) string {
	depth := len(transcript.nodes) + 1
	digest := transcriptNodeDigest(parent, messageDigest)
	var parentPointer *string
	if parent != "" {
		value := parent
		parentPointer = &value
	}
	transcript.nodes = append(transcript.nodes, storedTranscriptNode{
		digest: digest, parentDigest: parentPointer,
		messageDigest: messageDigest, depth: depth,
	})
	return digest
}

func transcriptNodeDigest(parent, messageDigest string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(transcriptNodeDomain))
	_, _ = hash.Write([]byte(parent))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(messageDigest))
	return hex.EncodeToString(hash.Sum(nil))
}

func encodeStoredMessage(message exchangecontent.Message) (string, []byte, error) {
	encoded, err := json.Marshal(message)
	if err != nil || len(encoded) == 0 || len(encoded) > exchangecontent.MaxEncodedBytes {
		return "", nil, exchangecontent.ErrInvalidEvidence
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), encoded, nil
}

func decodeStoredMessage(encoded []byte) (exchangecontent.Message, error) {
	var message exchangecontent.Message
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil {
		return exchangecontent.Message{}, exchangecontent.ErrInvalidEvidence
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return exchangecontent.Message{}, exchangecontent.ErrInvalidEvidence
	}
	canonical, err := json.Marshal(message)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return exchangecontent.Message{}, exchangecontent.ErrInvalidEvidence
	}
	return message, nil
}

func putStoredMessage(
	ctx context.Context,
	transaction *sql.Tx,
	digest string,
	payload []byte,
) error {
	return putStoredMessageBlocks(ctx, transaction, digest, payload)
}

func putStoredTranscriptNode(
	ctx context.Context,
	transaction *sql.Tx,
	node storedTranscriptNode,
) error {
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO runtime_exchange_content_transcripts(
		   digest, parent_digest, message_digest, depth
		 ) VALUES (?, ?, ?, ?) ON CONFLICT(digest) DO UPDATE
		 SET depth = excluded.depth
		 WHERE runtime_exchange_content_transcripts.parent_digest IS excluded.parent_digest
		   AND runtime_exchange_content_transcripts.message_digest = excluded.message_digest
		   AND runtime_exchange_content_transcripts.depth = excluded.depth`,
		node.digest,
		node.parentDigest,
		node.messageDigest,
		node.depth,
	)
	if err != nil {
		return fmt.Errorf("persist Exchange transcript node: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return exchangecontent.ErrInvalidEvidence
	}
	return nil
}

func findStoredTranscriptBase(
	ctx context.Context,
	transaction *sql.Tx,
	scopeKind, scopeID string,
	nodes []storedTranscriptNode,
	requestCount int,
) (string, int, error) {
	// A managed run is one launched client process. Manual captures can carry
	// unrelated sessions and therefore get content-addressed deduplication but
	// no inferred incremental presentation.
	if scopeKind != "managed_run" || scopeID == "" {
		return "", 0, nil
	}
	if requestCount < 1 || len(nodes) < requestCount {
		return "", 0, exchangecontent.ErrInvalidEvidence
	}
	candidates := make([]storedTranscriptCandidate, requestCount)
	for index, node := range nodes[:requestCount] {
		if node.depth != index+1 || node.digest == "" {
			return "", 0, exchangecontent.ErrInvalidEvidence
		}
		candidates[index] = storedTranscriptCandidate{
			Digest: node.digest,
			Depth:  node.depth,
		}
	}
	encodedCandidates, err := json.Marshal(candidates)
	if err != nil {
		return "", 0, exchangecontent.ErrInvalidEvidence
	}
	var digest string
	var depth int
	err = transaction.QueryRowContext(
		ctx,
		`WITH candidates(digest, depth) AS (
		   SELECT json_extract(value, '$.digest'),
		          json_extract(value, '$.depth')
		     FROM json_each(?)
		 )
		 SELECT candidates.digest, candidates.depth FROM candidates
		 WHERE EXISTS(SELECT 1 FROM runtime_exchange_contents AS contents
		   WHERE contents.scope_kind = ? AND contents.scope_id = ?
		     AND contents.expected_transcript_digest = candidates.digest
		     AND contents.expected_message_count = candidates.depth)
		 ORDER BY candidates.depth DESC
		  LIMIT 1`,
		string(encodedCandidates),
		scopeKind,
		scopeID,
	).Scan(&digest, &depth)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("resolve Exchange transcript base: %w", err)
	}
	if digest == "" || depth <= 0 || depth > requestCount {
		return "", 0, exchangecontent.ErrInvalidEvidence
	}
	return digest, depth, nil
}

func loadStoredTranscript(
	ctx context.Context,
	database *sql.DB,
	root string,
	wantCount int,
) ([]exchangecontent.Message, error) {
	rows, err := database.QueryContext(
		ctx,
		`WITH RECURSIVE chain(digest, parent_digest, message_digest, depth) AS (
		   SELECT digest, parent_digest, message_digest, depth
		     FROM runtime_exchange_content_transcripts WHERE digest = ?
		   UNION ALL
		   SELECT nodes.digest, nodes.parent_digest, nodes.message_digest, nodes.depth
		     FROM runtime_exchange_content_transcripts AS nodes
		     JOIN chain ON nodes.digest = chain.parent_digest
		 )
		 SELECT chain.depth, chain.message_digest
		   FROM chain
		  ORDER BY chain.depth ASC`,
		root,
	)
	if err != nil {
		return nil, fmt.Errorf("load Exchange transcript: %w", err)
	}
	digests := make([]string, 0, wantCount)
	for rows.Next() {
		var depth int
		var digest string
		if err := rows.Scan(&depth, &digest); err != nil ||
			depth != len(digests)+1 {
			_ = rows.Close()
			return nil, exchangecontent.ErrInvalidEvidence
		}
		digests = append(digests, digest)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate Exchange transcript: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close Exchange transcript: %w", err)
	}
	// Resolved after the chain walk, and in two queries rather than one per
	// message plus one per block: the store keeps a single connection, and a long
	// transcript otherwise held it for thousands of sequential round trips.
	resolved, err := loadStoredMessagesByDigest(ctx, database, digests)
	if err != nil {
		return nil, err
	}
	messages := make([]exchangecontent.Message, 0, wantCount)
	for _, digest := range digests {
		message, ok := resolved[digest]
		if !ok {
			return nil, exchangecontent.ErrInvalidEvidence
		}
		messages = append(messages, message)
	}
	if len(messages) != wantCount {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	return messages, nil
}

func loadStoredTranscriptSuffix(
	ctx context.Context,
	database *sql.DB,
	root string,
	baseRoot sql.NullString,
	inherited int,
	total int,
) ([]exchangecontent.Message, error) {
	if inherited < 0 || inherited > total || total < 1 || !validStoredDigest(root) {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	if inherited > 0 {
		if !baseRoot.Valid || !validStoredDigest(baseRoot.String) {
			return nil, exchangecontent.ErrInvalidEvidence
		}
		var depth int
		if err := database.QueryRowContext(
			ctx,
			`SELECT depth FROM runtime_exchange_content_transcripts WHERE digest = ?`,
			baseRoot.String,
		).Scan(&depth); err != nil || depth != inherited {
			return nil, exchangecontent.ErrInvalidEvidence
		}
	} else if baseRoot.Valid {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	if inherited == total {
		if baseRoot.String != root {
			return nil, exchangecontent.ErrInvalidEvidence
		}
		return []exchangecontent.Message{}, nil
	}

	rows, err := database.QueryContext(
		ctx,
		`WITH RECURSIVE chain(
		   digest, parent_digest, message_digest, depth
		 ) AS (
		   SELECT digest, parent_digest, message_digest, depth
		     FROM runtime_exchange_content_transcripts WHERE digest = ?
		   UNION ALL
		   SELECT nodes.digest, nodes.parent_digest, nodes.message_digest, nodes.depth
		     FROM runtime_exchange_content_transcripts AS nodes
		     JOIN chain ON nodes.digest = chain.parent_digest
		    WHERE chain.depth > ?
		 )
		 SELECT chain.digest, chain.parent_digest, chain.message_digest,
		        chain.depth
		   FROM chain
		  ORDER BY chain.depth ASC`,
		root,
		inherited+1,
	)
	if err != nil {
		return nil, fmt.Errorf("load Exchange transcript suffix: %w", err)
	}
	wantCount := total - inherited
	messageDigests := make([]string, 0, wantCount)
	previousDigest := baseRoot.String
	for rows.Next() {
		var digest, messageDigest string
		var parentDigest sql.NullString
		var depth int
		if err := rows.Scan(
			&digest, &parentDigest, &messageDigest, &depth,
		); err != nil {
			_ = rows.Close()
			return nil, exchangecontent.ErrInvalidEvidence
		}
		wantDepth := inherited + len(messageDigests) + 1
		if depth != wantDepth || parentDigest.String != previousDigest ||
			(parentDigest.Valid != (previousDigest != "")) ||
			!validStoredDigest(digest) || !validStoredDigest(messageDigest) ||
			digest != transcriptNodeDigest(previousDigest, messageDigest) {
			_ = rows.Close()
			return nil, exchangecontent.ErrInvalidEvidence
		}
		messageDigests = append(messageDigests, messageDigest)
		previousDigest = digest
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate Exchange transcript suffix: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close Exchange transcript suffix: %w", err)
	}
	if len(messageDigests) != wantCount || previousDigest != root {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	resolved, err := loadStoredMessagesByDigest(ctx, database, messageDigests)
	if err != nil {
		return nil, err
	}
	messages := make([]exchangecontent.Message, 0, wantCount)
	for _, messageDigest := range messageDigests {
		message, ok := resolved[messageDigest]
		if !ok {
			return nil, exchangecontent.ErrInvalidEvidence
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func loadStoredResponseMessage(
	ctx context.Context,
	database *sql.DB,
	digest sql.NullString,
) (*exchangecontent.Message, error) {
	if !digest.Valid {
		return nil, nil
	}
	message, err := loadStoredMessage(ctx, database, digest.String)
	if err != nil || message.Role != "assistant" {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	return &message, nil
}

// loadStoredSystemBlocks reads the top-level instruction parameter. It is
// stored as a content-addressed message so identical instructions cost one row,
// but it returns blocks: the parameter is a per-request field of the Request,
// never a message in its transcript.
func loadStoredSystemBlocks(
	ctx context.Context,
	database *sql.DB,
	digest sql.NullString,
) ([]exchangecontent.Block, error) {
	if !digest.Valid {
		return nil, nil
	}
	message, err := loadStoredMessage(ctx, database, digest.String)
	if err != nil || message.Role != "system" || len(message.Blocks) == 0 {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	return message.Blocks, nil
}

func loadStoredMessage(
	ctx context.Context,
	database *sql.DB,
	digest string,
) (exchangecontent.Message, error) {
	return loadStoredMessageBlocks(ctx, database, digest)
}

func storedContentScope(parent exchangecontent.ParentRef) (string, string) {
	switch {
	case parent.CaptureRunID != "":
		return "managed_run", parent.CaptureRunID
	case parent.ManualCaptureID != "":
		return "manual_capture", parent.ManualCaptureID
	default:
		return "", ""
	}
}

func cloneStoredBlocks(blocks []exchangecontent.Block) []exchangecontent.Block {
	result := make([]exchangecontent.Block, len(blocks))
	for index, block := range blocks {
		result[index] = block
		result[index].Arguments = append(json.RawMessage(nil), block.Arguments...)
		if block.Agent != nil {
			context := *block.Agent
			result[index].Agent = &context
		}
	}
	return result
}

func nullableDigest(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func validStoredDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
