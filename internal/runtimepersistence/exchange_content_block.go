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

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

const storedDigestHexBytes = 2 * sha256.Size

// putStoredMessageBlocks writes a message as its role, agent context and an
// ordered manifest of content-block digests.
//
// The message digest is not recomputed here: it stays SHA-256 of the message's
// canonical JSON, so every transcript node and every stored digest keeps the
// meaning it already had. Only where the bytes live changes.
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
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO runtime_exchange_content_messages(
		   digest, role, agent_json, block_manifest
		 ) VALUES (?, ?, ?, ?) ON CONFLICT(digest) DO UPDATE
		 SET digest = excluded.digest
		 WHERE runtime_exchange_content_messages.role = excluded.role
		   AND runtime_exchange_content_messages.agent_json IS excluded.agent_json
		   AND runtime_exchange_content_messages.block_manifest =
		       excluded.block_manifest`,
		digest, message.Role, agent, string(manifest),
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
		 SET digest = excluded.digest
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
		   FROM runtime_exchange_content_blocks WHERE digest = ?`,
		digest,
	).Scan(&plainBytes, &codec, &stored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return exchangecontent.Block{}, exchangecontent.ErrInvalidEvidence
		}
		return exchangecontent.Block{}, fmt.Errorf(
			"load Exchange content block: %w", err,
		)
	}
	return decodeStoredBlockPayload(plainBytes, codec, stored)
}

// decodeStoredBlockPayload turns one stored row into a block. plainBytes is
// bounded before it reaches an allocation, because this database is deliberately
// unprotected at rest and the value arrives from it.
func decodeStoredBlockPayload(
	plainBytes int,
	codec string,
	stored []byte,
) (exchangecontent.Block, error) {
	if plainBytes > exchangecontent.MaxEncodedBytes {
		return exchangecontent.Block{}, exchangecontent.ErrInvalidEvidence
	}
	switch codec {
	case chunkCodecIdentity:
	case chunkCodecZstd:
		decoded, err := bodyDecoder.DecodeAll(stored, make([]byte, 0, plainBytes))
		if err != nil {
			return exchangecontent.Block{}, fmt.Errorf(
				"decompress Exchange content block: %w", err,
			)
		}
		stored = decoded
	default:
		return exchangecontent.Block{}, exchangecontent.ErrInvalidEvidence
	}
	if len(stored) != plainBytes {
		return exchangecontent.Block{}, exchangecontent.ErrInvalidEvidence
	}
	return decodeStoredBlock(stored)
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

// purgeUnreferencedContentBlocks releases blocks no retained message names.
func purgeUnreferencedContentBlocks(
	ctx context.Context,
	transaction *sql.Tx,
) error {
	if _, err := transaction.ExecContext(
		ctx,
		// Recursing over the message digest rather than its manifest, for the
		// same reason as the raw plane: carrying the manifest makes the sweep
		// quadratic in blocks per message.
		`WITH RECURSIVE spans(message_digest, position) AS (
		   SELECT digest, 1 FROM runtime_exchange_content_messages
		   UNION ALL
		   SELECT spans.message_digest, spans.position + 64
		     FROM spans
		     JOIN runtime_exchange_content_messages AS messages
		       ON messages.digest = spans.message_digest
		    WHERE spans.position + 64 <= length(messages.block_manifest)
		 )
		 DELETE FROM runtime_exchange_content_blocks
		  WHERE digest NOT IN (
		    SELECT substr(messages.block_manifest, spans.position, 64)
		      FROM spans
		      JOIN runtime_exchange_content_messages AS messages
		        ON messages.digest = spans.message_digest
		  )`,
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
		   JOIN json_each(?) AS wanted ON wanted.value = messages.digest`,
		string(wanted),
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
		   JOIN json_each(?) AS wanted ON wanted.value = blocks.digest`,
		string(wanted),
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
		block, err := decodeStoredBlockPayload(plainBytes, codec, stored)
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
