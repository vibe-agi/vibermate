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

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const storedDigestHexBytes = 2 * sha256.Size

// One ledger lives for one returned representation, not for one decoder call.
// Logical Source cost and actual retained output capacity have distinct totals.
type storedReadLedger struct {
	limits  exchangecontent.SourceLimits
	logical storedBlockLogicalCost
	live    uint64
	bound   uint64
}

func (l *storedReadLedger) admit(c storedDecodeCost) error {
	if c.LogicalCost.RetainedBytes > l.limits.RetainedBytes-l.logical.RetainedBytes || c.LogicalCost.StructureBytes > l.limits.StructureBytes-l.logical.StructureBytes {
		return exchangecontent.ErrInvalidEvidence
	}
	left := l.bound - l.live
	for _, n := range []uint64{c.RetainedBytes, c.RetainedStructureBytes, c.WorkspaceBytes, 6 * exchangecontent.MaxEncodedBytes} {
		if n > left {
			return exchangecontent.ErrInvalidEvidence
		}
		left -= n
	}
	return nil
}
func (l *storedReadLedger) commit(c storedDecodeCost, retained bool) error {
	if err := l.admit(c); err != nil {
		return err
	}
	l.logical.RetainedBytes += c.LogicalCost.RetainedBytes
	l.logical.StructureBytes += c.LogicalCost.StructureBytes
	if retained {
		// Four cell copies cover slice capacity/growth overlap and the typed
		// presentation clone. The full result separately reserves a second raw
		// copy below; ordinary strings already use the parser's 2*n.
		if err := l.hold(c.RetainedBytes, 4*c.RetainedStructureBytes, 4096); err != nil {
			return err
		}
	}
	return nil
}

func (l *storedReadLedger) hold(amounts ...uint64) error {
	for _, n := range amounts {
		if n > l.bound-l.live {
			return exchangecontent.ErrInvalidEvidence
		}
		l.live += n
	}
	return nil
}

func (l *storedReadLedger) holdShell() error {
	// At most 24 returned blocks/messages plus system/response markers are
	// constructed by one Store page. Reserve both cursor encoding and retained
	// bytes, plus cell/growth capacity, before creating any shell or map entry.
	return l.hold(2*exchangecontent.MaxPageCursorBytes+exchangecontent.MaxExchangeIDBytes, 4*uint64(reflect.TypeFor[exchangecontent.Block]().Size()+reflect.TypeFor[exchangecontent.DeferredContent]().Size()+reflect.TypeFor[exchangecontent.Message]().Size()))
}
func (l *storedReadLedger) parent(payload, structure uint64) error {
	if payload > l.limits.RetainedBytes-l.logical.RetainedBytes || structure > l.limits.StructureBytes-l.logical.StructureBytes || payload > (l.bound-l.live)/2 {
		return exchangecontent.ErrInvalidEvidence
	}
	l.logical.RetainedBytes += payload
	l.logical.StructureBytes += structure
	l.live += 2 * payload
	if structure > (l.bound-l.live)/4 {
		return exchangecontent.ErrInvalidEvidence
	}
	l.live += 4 * structure
	return nil
}

func decodeStoredAgent(encoded []byte) (*exchangecontent.AgentContext, error) {
	if len(encoded) == 0 {
		return nil, nil
	}
	if len(encoded) > 4096 {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	var agent exchangecontent.AgentContext
	d := json.NewDecoder(bytes.NewReader(encoded))
	d.DisallowUnknownFields()
	if err := d.Decode(&agent); err != nil {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	if err := (protocolcore.AgentMessageContext{AgentName: agent.AgentName, Author: agent.Author, Recipient: agent.Recipient}).Validate(); err != nil {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	canonical, err := storedAgentBytes(&agent)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	return &agent, nil
}

func (repository *exchangeContentRepository) physicalContentRow(ctx context.Context, digest string) (int, string, []byte, error) {
	var n int
	var codec string
	var data []byte
	// payload itself has no schema upper CHECK. Restrict length before Scan so
	// a damaged DB cannot force an unbounded driver allocation from plain_bytes.
	err := repository.reads.QueryRowContext(ctx, `SELECT plain_bytes,codec,payload FROM runtime_exchange_content_blocks WHERE digest=? AND plain_bytes BETWEEN 1 AND ? AND length(payload) BETWEEN 1 AND ? AND codec IN ('identity','zstd')`, digest, exchangecontent.MaxEncodedBytes, exchangecontent.MaxEncodedBytes).Scan(&n, &codec, &data)
	if errors.Is(err, sql.ErrNoRows) {
		err = exchangecontent.ErrInvalidEvidence
	}
	return n, codec, data, err
}

type storedMessageReadRequest struct {
	First    int
	Body     bool
	Offset   uint64
	Kind     string
	Budget   *int
	Envelope uint16
}
type storedMessageReadResult struct {
	Message     exchangecontent.Message
	Total, Next int
	Body        storedBodyRange
	Detail      storedDetailPage
	Deferred    map[int]uint64
	InlineDebit int
}

type storedMessageMetadata struct {
	role, manifest string
	agent          []byte
}

func (repository *exchangeContentRepository) contentMessageMetadata(ctx context.Context, digests []string) (map[string]storedMessageMetadata, error) {
	if len(digests) == 0 || len(digests) > exchangecontent.PageMessageLimit {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	wanted := uniqueStrings(digests)
	encoded, err := json.Marshal(wanted)
	if err != nil {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	rows, err := repository.reads.QueryContext(ctx, `SELECT digest,role,agent_json,block_manifest FROM runtime_exchange_content_messages JOIN json_each(?) wanted ON wanted.value=digest WHERE length(CAST(role AS BLOB))<=32 AND coalesce(length(agent_json),0)<=4096 AND length(CAST(block_manifest AS BLOB)) BETWEEN 64 AND ? AND length(CAST(block_manifest AS BLOB))%64=0`, string(encoded), storedManifestSlots*storedDigestHexBytes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]storedMessageMetadata, len(wanted))
	for rows.Next() {
		var digest string
		var m storedMessageMetadata
		if err := rows.Scan(&digest, &m.role, &m.agent, &m.manifest); err != nil {
			return nil, err
		}
		result[digest] = m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(wanted) {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	return result, nil
}

func (repository *exchangeContentRepository) readVerifiedMessage(ctx context.Context, digest string, mode environment.ContentRecordingMode, ledger *storedReadLedger, header bool) (exchangecontent.Message, error) {
	result, err := repository.readSelectedMessage(ctx, digest, mode, ledger, header, nil)
	return result.Message, err
}

func (repository *exchangeContentRepository) readSelectedMessage(ctx context.Context, digest string, mode environment.ContentRecordingMode, ledger *storedReadLedger, header bool, request *storedMessageReadRequest) (storedMessageReadResult, error) {
	metadata, err := repository.contentMessageMetadata(ctx, []string{digest})
	if err != nil {
		return storedMessageReadResult{}, err
	}
	return repository.readMessageMetadata(ctx, digest, metadata[digest], mode, ledger, header, request)
}

func (repository *exchangeContentRepository) readMessageMetadata(ctx context.Context, digest string, metadata storedMessageMetadata, mode environment.ContentRecordingMode, ledger *storedReadLedger, header bool, request *storedMessageReadRequest) (storedMessageReadResult, error) {
	zero := storedMessageReadResult{}
	role, manifest, encodedAgent := metadata.role, metadata.manifest, metadata.agent
	agent, err := decodeStoredAgent(encodedAgent)
	if err != nil {
		return zero, err
	}
	switch protocolcore.Role(role) {
	case protocolcore.RoleSystem, protocolcore.RoleDeveloper, protocolcore.RoleUser, protocolcore.RoleAssistant, protocolcore.RoleTool:
	default:
		return zero, exchangecontent.ErrInvalidEvidence
	}
	if header {
		n, s := uint64(len(role)), uint64(reflect.TypeFor[exchangecontent.Message]().Size())
		if agent != nil {
			n += uint64(len(agent.AgentName) + len(agent.Author) + len(agent.Recipient))
			s += uint64(reflect.TypeFor[exchangecontent.AgentContext]().Size())
		}
		if err := ledger.parent(n, s); err != nil {
			return zero, err
		}
	} else if agent != nil {
		return zero, exchangecontent.ErrInvalidEvidence
	}
	h := sha256.New()
	roleJSON, _ := json.Marshal(role)
	_, _ = h.Write([]byte(`{"role":`))
	_, _ = h.Write(roleJSON)
	_, _ = h.Write([]byte(`,"blocks":[`))
	message := exchangecontent.Message{Role: role, Agent: agent}
	result := storedMessageReadResult{Next: -1}
	budget := exchangecontent.PageContentBytes
	envelope := uint16(3) // ContentPage.blocks[].arguments.
	if request != nil {
		if request.Budget != nil {
			budget = *request.Budget
		}
		if request.Envelope != 0 {
			envelope = request.Envelope
		}
	}
	initialBudget := budget
	d, err := newStoredBlockDecoder(storedDecodeLimits{CanonicalBytes: ledger.limits.CanonicalBytes, RetainedBytes: ledger.bound, RetainedStructureBytes: ledger.bound, WorkspaceBytes: storedDecodeFixedWorkspace + 2*maxNestingDepth*8}, ledger.admit)
	if err != nil {
		return zero, err
	}
	for slot := 0; slot < len(manifest)/storedDigestHexBytes; {
		index := result.Total
		if index > 0 {
			_, _ = h.Write([]byte{','})
		}
		start, opens, slots := slot, 0, 0
		pending, err := newStoredLogicalBlockReader(ctx, manifest, start, ledger.limits.CanonicalBytes, repository.loadPhysical)
		if err != nil {
			return zero, err
		}
		canonicalSize := pending.Size
		open := func(ctx context.Context) (storedDecodeInput, error) {
			opens++
			if opens > 2 {
				return storedDecodeInput{}, exchangecontent.ErrInvalidEvidence
			}
			r := pending
			pending = nil // Do not retain pass one's physical row through pass two.
			if opens == 2 {
				var err error
				r, err = newStoredLogicalBlockReader(ctx, manifest, start, ledger.limits.CanonicalBytes, repository.loadPhysical)
				if err != nil {
					return storedDecodeInput{}, err
				}
			}
			slots = r.Slots
			var stream io.Reader = r
			if opens == 1 {
				stream = io.TeeReader(r, h)
			}
			return storedDecodeInput{Reader: stream, Size: r.Size, Digest: r.Digest, Slots: r.Slots}, nil
		}
		var block exchangecontent.Block
		retained, selected, deferred := request == nil, false, false
		if request != nil && index >= request.First && !request.Body && result.Next < 0 {
			if len(message.Blocks) >= exchangecontent.PageMessageLimit || (canonicalSize <= exchangecontent.PageContentBytes && canonicalSize > uint64(max(budget, 0))) || budget < 1024 {
				result.Next = index
			} else if canonicalSize > exchangecontent.PageContentBytes {
				deferred = true
			} else {
				retained = true
			}
		}
		if request != nil && request.Body && index == request.First {
			selected = true
			retained = true
		}
		if selected {
			kind := request.Kind
			if kind == "" {
				kind = "body"
			}
			result.Detail, err = d.detail(ctx, open, mode, kind, request.Offset)
			result.Body = result.Detail.Body
		} else if retained && request != nil {
			var inline *storedDecodeResult
			inline, err = d.decode(ctx, open, mode, storedDecodeRequest{inline: true, inlineBudget: uint64(max(budget, 0)), inlineEnvelope: envelope})
			if err == nil {
				deferred = inline.deferred
				retained = !deferred
				if retained {
					block = inline.block()
					budget -= int(inline.inlineDebit)
				}
			}
		} else if retained {
			block, err = d.full(ctx, open, mode)
		} else {
			_, err = d.count(ctx, open, mode)
		}
		if err != nil {
			return zero, err
		}
		if opens != 2 || slots < 1 {
			return zero, exchangecontent.ErrInvalidEvidence
		}
		if err := ledger.commit(d.cost, retained); err != nil {
			return zero, err
		}
		if retained && !selected {
			if err := ledger.hold(uint64(len(block.Arguments))); err != nil {
				return zero, err
			}
			message.Blocks = append(message.Blocks, block)
			if request == nil {
				budget -= int(canonicalSize)
			}
		}
		if deferred {
			if err := ledger.holdShell(); err != nil {
				return zero, err
			}
			if result.Deferred == nil {
				result.Deferred = make(map[int]uint64)
			}
			result.Deferred[len(message.Blocks)] = canonicalSize
			message.Blocks = append(message.Blocks, exchangecontent.Block{})
			budget -= 1024
		}
		result.Total++
		slot += slots
	}
	_, _ = h.Write([]byte{']'})
	if agent != nil {
		legacy, _ := json.Marshal(agent)
		_, _ = h.Write([]byte(`,"agent":`))
		_, _ = h.Write(legacy)
	}
	_, _ = h.Write([]byte{'}'})
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return zero, exchangecontent.ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if request != nil && (request.First < 0 || request.First >= result.Total || request.Body && len(result.Body.Bytes) == 0) {
		return zero, exchangecontent.ErrInvalidEvidence
	}
	result.Message = message
	result.InlineDebit = initialBudget - budget
	return result, nil
}

// putStoredMessageBlocks writes a message as its role, agent context and an
// ordered manifest of content-block digests.
//
// The message digest is not recomputed here: it stays SHA-256 of the message's
// canonical JSON, so every transcript node and every stored digest keeps the
// meaning it already had. Only where the bytes live changes.
// On conflict, update only an equal non-key field: assigning digest to itself
// makes SQLite recheck every retained foreign-key reference on every append.
func putStoredMessageBlocks(
	ctx context.Context,
	transaction *sql.Tx,
	digest string,
	payload []byte,
) error {
	message, err := decodeStoredMessage(payload)
	if err != nil {
		return err
	}
	manifest := make([]byte, 0, len(message.Blocks)*storedDigestHexBytes)
	for _, block := range message.Blocks {
		blockDigest, encoded, encodeErr := encodeStoredBlock(block)
		if encodeErr != nil {
			return encodeErr
		}
		manifest = append(manifest, blockDigest...)
		if err := putStoredBlock(
			ctx, transaction, blockDigest, encoded,
		); err != nil {
			return err
		}
	}
	if len(manifest) == 0 {
		return exchangecontent.ErrInvalidEvidence
	}
	var agent any
	if message.Agent != nil {
		encodedAgent, marshalErr := json.Marshal(message.Agent)
		if marshalErr != nil {
			return exchangecontent.ErrInvalidEvidence
		}
		agent = encodedAgent
	}
	return putStoredMessageManifest(ctx, transaction, digest, message.Role, agent, string(manifest))
}

func putStoredMessageManifest(ctx context.Context, transaction *sql.Tx, digest, role string, agent any, manifest string) error {
	if data, ok := agent.([]byte); ok && len(data) == 0 {
		agent = nil
	}
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO runtime_exchange_content_messages(
		   digest, role, agent_json, block_manifest
		 ) VALUES (?, ?, ?, ?) ON CONFLICT(digest) DO UPDATE
		 SET role = excluded.role
		 WHERE runtime_exchange_content_messages.role = excluded.role
		   AND runtime_exchange_content_messages.agent_json IS excluded.agent_json
		   AND runtime_exchange_content_messages.block_manifest =
		       excluded.block_manifest`,
		digest, role, agent, manifest,
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

// putStoredBlock writes one block unconditionally, for the same reason the
// raw-evidence chunk writer does: a check-then-skip could have the row purged
// before the referencing message commits.
//
// As there, the conflict guard is a length check rather than a byte comparison,
// because the codec is not part of a block's identity. Byte integrity is proven
// on read: the rebuilt message is re-canonicalized and must hash to the digest
// it was stored under.
func putStoredBlock(
	ctx context.Context,
	transaction *sql.Tx,
	digest string,
	encoded []byte,
) error {
	codec := chunkCodecZstd
	stored := bodyEncoder.EncodeAll(encoded, nil)
	if len(stored) >= len(encoded) {
		codec = chunkCodecIdentity
		stored = encoded
	}
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO runtime_exchange_content_blocks(
		   digest, plain_bytes, codec, payload
		 ) VALUES (?, ?, ?, ?) ON CONFLICT(digest) DO UPDATE
		 SET plain_bytes = excluded.plain_bytes
		 WHERE runtime_exchange_content_blocks.plain_bytes = excluded.plain_bytes`,
		digest, len(encoded), codec, stored,
	)
	if err != nil {
		return fmt.Errorf("persist Exchange content block: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errors.New("stored Exchange content block disagrees with its digest")
	}
	return nil
}

// loadStoredMessageBlocks rebuilds a message and requires the result to hash to
// the digest it was stored under. That is the same round-trip guarantee the
// single-payload store gave, applied one level down.
func loadStoredMessageBlocks(
	ctx context.Context,
	queryer interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	digest string,
) (exchangecontent.Message, error) {
	var role string
	var agent []byte
	var manifest string
	if err := queryer.QueryRowContext(
		ctx,
		`SELECT role, agent_json, block_manifest
		   FROM runtime_exchange_content_messages WHERE digest = ?`,
		digest,
	).Scan(&role, &agent, &manifest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return exchangecontent.Message{}, exchangecontent.ErrInvalidEvidence
		}
		return exchangecontent.Message{}, fmt.Errorf(
			"load Exchange content message: %w", err,
		)
	}
	if len(manifest)%storedDigestHexBytes != 0 || manifest == "" {
		return exchangecontent.Message{}, exchangecontent.ErrInvalidEvidence
	}
	message := exchangecontent.Message{Role: role}
	if len(agent) > 0 {
		var context exchangecontent.AgentContext
		decoder := json.NewDecoder(bytes.NewReader(agent))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&context); err != nil {
			return exchangecontent.Message{}, exchangecontent.ErrInvalidEvidence
		}
		message.Agent = &context
	}
	for offset := 0; offset < len(manifest); offset += storedDigestHexBytes {
		block, err := loadStoredBlock(
			ctx, queryer, manifest[offset:offset+storedDigestHexBytes],
		)
		if err != nil {
			return exchangecontent.Message{}, err
		}
		message.Blocks = append(message.Blocks, block)
	}
	rebuilt, _, err := encodeStoredMessage(message)
	if err != nil || rebuilt != digest {
		return exchangecontent.Message{}, exchangecontent.ErrInvalidEvidence
	}
	return message, nil
}

func loadStoredBlock(
	ctx context.Context,
	queryer interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	digest string,
) (exchangecontent.Block, error) {
	var plainBytes int
	var codec string
	var stored []byte
	if err := queryer.QueryRowContext(
		ctx,
		`SELECT plain_bytes, codec, payload
		   FROM runtime_exchange_content_blocks WHERE digest = ?
		   AND plain_bytes BETWEEN 1 AND ? AND length(payload)<=?
		   AND codec IN ('identity','zstd')`,
		digest, exchangecontent.MaxEncodedBytes, exchangecontent.MaxEncodedBytes,
	).Scan(&plainBytes, &codec, &stored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return exchangecontent.Block{}, exchangecontent.ErrInvalidEvidence
		}
		return exchangecontent.Block{}, fmt.Errorf(
			"load Exchange content block: %w", err,
		)
	}
	plain, err := decodeStoredPhysicalPayload(digest, plainBytes, codec, stored)
	if err != nil {
		return exchangecontent.Block{}, err
	}
	return decodeStoredBlock(plain)
}

func encodeStoredBlock(block exchangecontent.Block) (string, []byte, error) {
	encoded, err := json.Marshal(block)
	if err != nil || len(encoded) == 0 ||
		len(encoded) > exchangecontent.MaxEncodedBytes {
		return "", nil, exchangecontent.ErrInvalidEvidence
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), encoded, nil
}

func decodeStoredBlock(encoded []byte) (exchangecontent.Block, error) {
	var block exchangecontent.Block
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&block); err != nil {
		return exchangecontent.Block{}, exchangecontent.ErrInvalidEvidence
	}
	canonical, err := json.Marshal(block)
	if err != nil || string(canonical) != string(encoded) {
		return exchangecontent.Block{}, exchangecontent.ErrInvalidEvidence
	}
	return block, nil
}

// Only the deleted message's blocks can have become unreferenced.
func purgeUnreferencedContentBlocks(
	ctx context.Context,
	transaction *sql.Tx,
	manifest string,
) error {
	if _, err := transaction.ExecContext(
		ctx,
		`WITH RECURSIVE spans(position) AS (
		   VALUES(1)
		   UNION ALL
		   SELECT position + 64 FROM spans WHERE position + 64 <= length(?)
		 )
		 DELETE FROM runtime_exchange_content_blocks
		  WHERE digest IN (SELECT substr(?, position, 64) FROM spans)
		  AND NOT EXISTS(SELECT 1 FROM runtime_exchange_content_block_refs
		    WHERE block_digest=runtime_exchange_content_blocks.digest)`, manifest, manifest,
	); err != nil {
		return fmt.Errorf("purge unreferenced Exchange content blocks: %w", err)
	}
	return nil
}

// loadStoredMessagesByDigest resolves many messages in two queries instead of
// one per message plus one per block.
//
// The store keeps a single connection, so a transcript read that issued a query
// per message and per block held it for thousands of sequential round trips and
// blocked every concurrent proxy write for the duration. Each message is still
// rebuilt and re-canonicalized against the digest it was stored under.
func loadStoredMessagesByDigest(
	ctx context.Context,
	database *sql.DB,
	digests []string,
) (map[string]exchangecontent.Message, error) {
	if len(digests) == 0 {
		return map[string]exchangecontent.Message{}, nil
	}
	wanted, err := json.Marshal(uniqueStrings(digests))
	if err != nil {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	type shell struct {
		role     string
		agent    []byte
		manifest string
	}
	shells := make(map[string]shell, len(digests))
	blockWanted := make([]string, 0, len(digests))
	rows, err := database.QueryContext(
		ctx,
		`SELECT messages.digest, messages.role, messages.agent_json,
		        messages.block_manifest
		   FROM runtime_exchange_content_messages AS messages
	   JOIN json_each(?) AS wanted ON wanted.value = messages.digest
	   WHERE length(messages.block_manifest) BETWEEN 64 AND ?
	     AND coalesce(length(messages.agent_json),0)<=4096`,
		string(wanted), 64*protocolcore.MaxContentBlocks,
	)
	if err != nil {
		return nil, fmt.Errorf("load Exchange content messages: %w", err)
	}
	for rows.Next() {
		var digest string
		var item shell
		if err := rows.Scan(
			&digest, &item.role, &item.agent, &item.manifest,
		); err != nil {
			_ = rows.Close()
			return nil, exchangecontent.ErrInvalidEvidence
		}
		if len(item.manifest)%storedDigestHexBytes != 0 || item.manifest == "" {
			_ = rows.Close()
			return nil, exchangecontent.ErrInvalidEvidence
		}
		for offset := 0; offset < len(item.manifest); offset += storedDigestHexBytes {
			blockWanted = append(
				blockWanted, item.manifest[offset:offset+storedDigestHexBytes],
			)
		}
		shells[digest] = item
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate Exchange content messages: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close Exchange content messages: %w", err)
	}

	blocks, err := loadStoredBlocksByDigest(ctx, database, blockWanted)
	if err != nil {
		return nil, err
	}
	messages := make(map[string]exchangecontent.Message, len(shells))
	for digest, item := range shells {
		message := exchangecontent.Message{Role: item.role}
		if len(item.agent) > 0 {
			var agent exchangecontent.AgentContext
			decoder := json.NewDecoder(bytes.NewReader(item.agent))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&agent); err != nil {
				return nil, exchangecontent.ErrInvalidEvidence
			}
			message.Agent = &agent
		}
		for offset := 0; offset < len(item.manifest); offset += storedDigestHexBytes {
			block, ok := blocks[item.manifest[offset:offset+storedDigestHexBytes]]
			if !ok {
				return nil, exchangecontent.ErrInvalidEvidence
			}
			message.Blocks = append(message.Blocks, block)
		}
		rebuilt, _, err := encodeStoredMessage(message)
		if err != nil || rebuilt != digest {
			return nil, exchangecontent.ErrInvalidEvidence
		}
		messages[digest] = message
	}
	return messages, nil
}

func loadStoredBlocksByDigest(
	ctx context.Context,
	database *sql.DB,
	digests []string,
) (map[string]exchangecontent.Block, error) {
	blocks := make(map[string]exchangecontent.Block, len(digests))
	if len(digests) == 0 {
		return blocks, nil
	}
	wanted, err := json.Marshal(uniqueStrings(digests))
	if err != nil {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	rows, err := database.QueryContext(
		ctx,
		`SELECT blocks.digest, blocks.plain_bytes, blocks.codec, blocks.payload
		   FROM runtime_exchange_content_blocks AS blocks
	   JOIN json_each(?) AS wanted ON wanted.value = blocks.digest
	   WHERE blocks.plain_bytes BETWEEN 1 AND ? AND length(blocks.payload)<=?
	     AND blocks.codec IN ('identity','zstd')`,
		string(wanted), exchangecontent.MaxEncodedBytes, exchangecontent.MaxEncodedBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("load Exchange content blocks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var digest, codec string
		var plainBytes int
		var stored []byte
		if err := rows.Scan(&digest, &plainBytes, &codec, &stored); err != nil {
			return nil, exchangecontent.ErrInvalidEvidence
		}
		plain, err := decodeStoredPhysicalPayload(digest, plainBytes, codec, stored)
		if err != nil {
			return nil, err
		}
		block, err := decodeStoredBlock(plain)
		if err != nil {
			return nil, err
		}
		blocks[digest] = block
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Exchange content blocks: %w", err)
	}
	return blocks, nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}
