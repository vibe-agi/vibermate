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
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

const transcriptNodeDomain = "vibermate:exchange-transcript-node\x00"

type exchangeContentRepository struct {
	database   *sql.DB
	operations *operationGate
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
	RequestedModel  string                           `json:"requestedModel"`
	EffectiveModel  string                           `json:"effectiveModel"`
	MaxOutputTokens int                              `json:"maxOutputTokens"`
	Stream          bool                             `json:"stream"`
	Tools           []exchangecontent.ToolDefinition `json:"tools"`
}

type storedResponseManifest struct {
	ID             string                `json:"id"`
	RequestedModel string                `json:"requestedModel"`
	EffectiveModel string                `json:"effectiveModel"`
	ReportedModel  string                `json:"reportedModel"`
	StopReason     string                `json:"stopReason"`
	Usage          exchangecontent.Usage `json:"usage"`
}

type storedTranscript struct {
	requestRoot    string
	expectedRoot   string
	requestCount   int
	expectedCount  int
	responseDigest string
	messages       map[string][]byte
	nodes          []storedTranscriptNode
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

var _ exchangecontent.Repository = (*exchangeContentRepository)(nil)

func newExchangeContentRepository(
	database *sql.DB,
	operations *operationGate,
) *exchangeContentRepository {
	return &exchangeContentRepository{database: database, operations: operations}
}

func (repository *exchangeContentRepository) Put(
	ctx context.Context,
	record exchangecontent.Record,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	manifest, encodedManifest, transcript, err := encodeStoredContent(record)
	if err != nil {
		return err
	}
	scopeKind, scopeID := storedContentScope(record.Parent)
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
	for index := inherited; index < len(transcript.nodes); index++ {
		node := transcript.nodes[index]
		payload, ok := transcript.messages[node.messageDigest]
		if !ok {
			return exchangecontent.ErrInvalidEvidence
		}
		if err := putStoredMessage(
			operation, transaction, node.messageDigest, payload,
		); err != nil {
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
		   response_message_digest, manifest_json
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
) (bool, error) {
	var storedScopeKind, storedScopeID, storedMode string
	var recordedMillis, expiresMillis int64
	var requestRoot, expectedRoot string
	var baseRoot, responseDigest sql.NullString
	var requestCount, expectedCount, inherited int
	var storedEncodedManifest []byte
	err := transaction.QueryRowContext(
		ctx,
		`SELECT scope_kind, scope_id, mode,
		        recorded_at_unix_ms, expires_at_unix_ms,
		        request_transcript_digest, expected_transcript_digest,
		        base_transcript_digest, request_message_count,
		        expected_message_count, inherited_message_count,
		        response_message_digest, manifest_json
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
		&storedEncodedManifest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load pending Exchange content: %w", err)
	}
	storedManifest, err := decodeStoredContentManifest(storedEncodedManifest)
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
		responseDigest.Valid || inherited < 0 || inherited > requestCount ||
		transcript.responseDigest == "" ||
		len(transcript.nodes) != transcript.expectedCount ||
		transcript.expectedCount != requestCount+1 {
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
	responseNode := transcript.nodes[len(transcript.nodes)-1]
	payload, ok := transcript.messages[responseNode.messageDigest]
	if !ok || responseNode.digest != transcript.expectedRoot {
		return false, exchangecontent.ErrInvalidEvidence
	}
	if err := putStoredMessage(ctx, transaction, responseNode.messageDigest, payload); err != nil {
		return false, err
	}
	if err := putStoredTranscriptNode(ctx, transaction, responseNode); err != nil {
		return false, err
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
		transcript.responseDigest,
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
	var scopeKind, scopeID, mode string
	var recordedMillis, expiresMillis int64
	var requestRoot, expectedRoot string
	var baseRoot, responseDigest sql.NullString
	var requestCount, expectedCount, inherited int
	var encodedManifest []byte
	err = repository.database.QueryRowContext(
		operation,
		`SELECT scope_kind, scope_id, mode,
		        recorded_at_unix_ms, expires_at_unix_ms,
		        request_transcript_digest, expected_transcript_digest,
		        base_transcript_digest, request_message_count,
		        expected_message_count, inherited_message_count,
		        response_message_digest, manifest_json
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
		&requestRoot,
		&expectedRoot,
		&baseRoot,
		&requestCount,
		&expectedCount,
		&inherited,
		&responseDigest,
		&encodedManifest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return exchangecontent.Record{}, exchangecontent.ErrNotFound
	}
	if err != nil {
		return exchangecontent.Record{}, fmt.Errorf("load Exchange content manifest: %w", err)
	}
	manifest, err := decodeStoredContentManifest(encodedManifest)
	if err != nil || manifest.ExchangeID != exchangeID || string(manifest.Mode) != mode ||
		toUnixMillis(manifest.RecordedAt) != recordedMillis ||
		toUnixMillis(manifest.ExpiresAt) != expiresMillis {
		return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
	}
	wantScopeKind, wantScopeID := storedContentScope(manifest.Parent)
	if scopeKind != wantScopeKind || scopeID != wantScopeID || requestCount < 1 ||
		expectedCount < requestCount || inherited < 0 || inherited > requestCount {
		return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
	}
	messages, err := loadStoredTranscript(
		operation,
		repository.database,
		requestRoot,
		requestCount,
	)
	if err != nil {
		return exchangecontent.Record{}, err
	}
	var responseMessage *exchangecontent.Message
	if responseDigest.Valid {
		message, loadErr := loadStoredMessage(operation, repository.database, responseDigest.String)
		if loadErr != nil || message.Role != "assistant" {
			return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
		}
		responseMessage = &message
	}
	record, err := recordFromStoredManifest(manifest, messages, responseMessage)
	if err != nil {
		return exchangecontent.Record{}, err
	}
	rebuilt, err := buildStoredTranscript(record)
	if err != nil || rebuilt.requestRoot != requestRoot ||
		rebuilt.expectedRoot != expectedRoot ||
		rebuilt.requestCount != requestCount ||
		rebuilt.expectedCount != expectedCount ||
		rebuilt.responseDigest != responseDigest.String {
		return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
	}
	if inherited == 0 {
		if baseRoot.Valid {
			return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
		}
		record.Presentation = exchangecontent.RequestPresentation{
			Mode: exchangecontent.RequestPresentationCheckpoint,
		}
	} else {
		if !baseRoot.Valid || inherited > len(rebuilt.nodes) ||
			rebuilt.nodes[inherited-1].digest != baseRoot.String {
			return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
		}
		presentationMode := exchangecontent.RequestPresentationIncremental
		if inherited == requestCount {
			presentationMode = exchangecontent.RequestPresentationSameTranscript
		}
		record.Presentation = exchangecontent.RequestPresentation{
			Mode: presentationMode, InheritedMessageCount: inherited,
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
	result, err := transaction.ExecContext(
		operation,
		`DELETE FROM runtime_exchange_contents WHERE expires_at_unix_ms <= ?`,
		toUnixMillis(now.UTC()),
	)
	if err != nil {
		return 0, fmt.Errorf("purge expired Exchange content evidence: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count < 0 {
		return 0, fmt.Errorf("read purged Exchange content evidence count: %w", err)
	}
	if _, err := transaction.ExecContext(
		operation,
		`WITH RECURSIVE reachable(digest) AS (
		   SELECT request_transcript_digest FROM runtime_exchange_contents
		   UNION
		   SELECT expected_transcript_digest FROM runtime_exchange_contents
		   UNION
		   SELECT base_transcript_digest FROM runtime_exchange_contents
		    WHERE base_transcript_digest IS NOT NULL
		   UNION
		   SELECT nodes.parent_digest
		     FROM runtime_exchange_content_transcripts AS nodes
		     JOIN reachable ON nodes.digest = reachable.digest
		    WHERE nodes.parent_digest IS NOT NULL
		 )
		 DELETE FROM runtime_exchange_content_transcripts
		 WHERE digest NOT IN (SELECT digest FROM reachable)`); err != nil {
		return 0, fmt.Errorf("purge unreferenced transcript nodes: %w", err)
	}
	if _, err := transaction.ExecContext(
		operation,
		`DELETE FROM runtime_exchange_content_messages
		 WHERE digest NOT IN (
		   SELECT message_digest FROM runtime_exchange_content_transcripts
		   UNION
		   SELECT response_message_digest FROM runtime_exchange_contents
		    WHERE response_message_digest IS NOT NULL
		 )`); err != nil {
		return 0, fmt.Errorf("purge unreferenced transcript messages: %w", err)
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
		},
	}
	if record.Response != nil {
		manifest.Response = &storedResponseManifest{
			ID:             record.Response.ID,
			RequestedModel: record.Response.RequestedModel,
			EffectiveModel: record.Response.EffectiveModel,
			ReportedModel:  record.Response.ReportedModel,
			StopReason:     record.Response.StopReason,
			Usage:          record.Response.Usage,
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
	if len(encoded) == 0 || len(encoded) > exchangecontent.MaxEncodedBytes {
		return storedExchangeContentManifest{}, exchangecontent.ErrInvalidEvidence
	}
	var manifest storedExchangeContentManifest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return storedExchangeContentManifest{}, exchangecontent.ErrInvalidEvidence
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return storedExchangeContentManifest{}, exchangecontent.ErrInvalidEvidence
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return storedExchangeContentManifest{}, exchangecontent.ErrInvalidEvidence
	}
	return manifest, nil
}

func recordFromStoredManifest(
	manifest storedExchangeContentManifest,
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
			Messages:        messages,
			Tools:           append([]exchangecontent.ToolDefinition(nil), manifest.Request.Tools...),
		},
	}
	if manifest.Response != nil {
		if responseMessage == nil {
			return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
		}
		record.Response = &exchangecontent.Response{
			ID:             manifest.Response.ID,
			RequestedModel: manifest.Response.RequestedModel,
			EffectiveModel: manifest.Response.EffectiveModel,
			ReportedModel:  manifest.Response.ReportedModel,
			StopReason:     manifest.Response.StopReason,
			Blocks:         cloneStoredBlocks(responseMessage.Blocks),
			Usage:          manifest.Response.Usage,
		}
	} else if responseMessage != nil {
		return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
	}
	if err := record.Validate(); err != nil {
		return exchangecontent.Record{}, err
	}
	return record, nil
}

func buildStoredTranscript(record exchangecontent.Record) (storedTranscript, error) {
	result := storedTranscript{messages: make(map[string][]byte)}
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
	if record.Response != nil {
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
	hash := sha256.New()
	_, _ = hash.Write([]byte(transcriptNodeDomain))
	_, _ = hash.Write([]byte(parent))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(messageDigest))
	digest := hex.EncodeToString(hash.Sum(nil))
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
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO runtime_exchange_content_messages(digest, payload_json)
		 VALUES (?, ?) ON CONFLICT(digest) DO UPDATE SET digest = excluded.digest
		 WHERE runtime_exchange_content_messages.payload_json = excluded.payload_json`,
		digest,
		payload,
	)
	if err != nil {
		return fmt.Errorf("persist Exchange content message: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return exchangecontent.ErrInvalidEvidence
	}
	return nil
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
		 SET digest = excluded.digest
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
		 SELECT contents.expected_transcript_digest,
		        candidates.depth
		   FROM runtime_exchange_contents AS contents
		   JOIN candidates
		     ON candidates.digest = contents.expected_transcript_digest
		  WHERE contents.scope_kind = ? AND contents.scope_id = ?
		    AND contents.expected_message_count = candidates.depth
		  ORDER BY candidates.depth DESC,
		           contents.recorded_at_unix_ms DESC
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
		 SELECT chain.depth, messages.digest, messages.payload_json
		   FROM chain
		   JOIN runtime_exchange_content_messages AS messages
		     ON messages.digest = chain.message_digest
		  ORDER BY chain.depth ASC`,
		root,
	)
	if err != nil {
		return nil, fmt.Errorf("load Exchange transcript: %w", err)
	}
	defer func() { _ = rows.Close() }()
	messages := make([]exchangecontent.Message, 0, wantCount)
	for rows.Next() {
		var depth int
		var digest string
		var encoded []byte
		if err := rows.Scan(&depth, &digest, &encoded); err != nil || depth != len(messages)+1 {
			return nil, exchangecontent.ErrInvalidEvidence
		}
		message, err := decodeStoredMessage(encoded)
		if err != nil {
			return nil, err
		}
		calculated, _, err := encodeStoredMessage(message)
		if err != nil || calculated != digest {
			return nil, exchangecontent.ErrInvalidEvidence
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Exchange transcript: %w", err)
	}
	if len(messages) != wantCount {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	return messages, nil
}

func loadStoredMessage(
	ctx context.Context,
	database *sql.DB,
	digest string,
) (exchangecontent.Message, error) {
	var encoded []byte
	if err := database.QueryRowContext(
		ctx,
		`SELECT payload_json FROM runtime_exchange_content_messages WHERE digest = ?`,
		digest,
	).Scan(&encoded); err != nil {
		return exchangecontent.Message{}, exchangecontent.ErrInvalidEvidence
	}
	message, err := decodeStoredMessage(encoded)
	if err != nil {
		return exchangecontent.Message{}, err
	}
	calculated, _, err := encodeStoredMessage(message)
	if err != nil || calculated != digest {
		return exchangecontent.Message{}, exchangecontent.ErrInvalidEvidence
	}
	return message, nil
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
	}
	return result
}

func nullableDigest(value string) any {
	if value == "" {
		return nil
	}
	return value
}
