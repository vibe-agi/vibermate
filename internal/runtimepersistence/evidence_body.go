package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/vibe-agi/vibermate/internal/evidencechunk"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

const (
	chunkCodecIdentity = "identity"
	chunkCodecZstd     = "zstd"
	// maximumChunkManifestBytes mirrors the schema bound: a 16 MiB body split at
	// a 2 KiB minimum yields at most 8,192 chunk digests.
	maximumChunkManifestBytes = 8192 * sha256.Size
)

// bodyEncoder and bodyDecoder are shared because they are expensive to construct
// and safe for concurrent use through EncodeAll and DecodeAll.
//
// Construction takes only compile-time constant options, so an error here is a
// programming mistake rather than a runtime condition. Failing at init makes that
// unmissable; returning an error instead put the guard at the caller, and a later
// caller — the content-block writer — was added without one and would have
// dereferenced nil.
var bodyEncoder = mustZstdWriter()
var bodyDecoder = mustZstdReader()

func mustZstdWriter() *zstd.Encoder {
	encoder, err := zstd.NewWriter(
		nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression),
	)
	if err != nil {
		panic(fmt.Errorf("construct evidence body encoder: %w", err))
	}
	return encoder
}

func mustZstdReader() *zstd.Decoder {
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		panic(fmt.Errorf("construct evidence body decoder: %w", err))
	}
	return decoder
}

// storeEvidenceBody writes the chunks of body and its manifest, returning the
// digest an envelope should reference. An empty body has no row and no digest.
func storeEvidenceBody(
	ctx context.Context,
	transaction *sql.Tx,
	body []byte,
) ([]byte, error) {
	if len(body) == 0 {
		return nil, nil
	}
	chunks := evidencechunk.Split(body)
	// Refused before any chunk is written, so an oversized body costs nothing.
	// Observation already clamps to DefaultMaximumBodyBytes, which cannot exceed
	// this bound; the check stays as the store's own guard.
	if len(chunks)*sha256.Size > maximumChunkManifestBytes {
		return nil, errors.New("evidence body manifest exceeds its bound")
	}
	manifest := make([]byte, 0, len(chunks)*sha256.Size)
	for _, chunk := range chunks {
		digest := sha256.Sum256(chunk)
		manifest = append(manifest, digest[:]...)
		if err := putEvidenceChunk(ctx, transaction, digest, chunk); err != nil {
			return nil, err
		}
	}
	bodyDigest := sha256.Sum256(body)
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO runtime_evidence_bodies(digest, plain_bytes, chunk_manifest)
		 VALUES (?, ?, ?) ON CONFLICT(digest) DO UPDATE SET digest = excluded.digest
		 WHERE runtime_evidence_bodies.plain_bytes = excluded.plain_bytes
		   AND runtime_evidence_bodies.chunk_manifest = excluded.chunk_manifest`,
		bodyDigest[:], len(body), manifest,
	)
	if err != nil {
		return nil, fmt.Errorf("persist evidence body: %w", err)
	}
	// The conflict predicate can fail, and a silently unwritten body row would
	// leave the envelope referencing a stale manifest whose chunks the next sweep
	// deletes. Every other content-addressed writer checks this; this one did not.
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, errors.New("stored evidence body disagrees with its digest")
	}
	return bodyDigest[:], nil
}

// putEvidenceChunk writes one chunk unconditionally.
//
// It must never check-then-skip. The raw evidence writer commits asynchronously
// while purge deletes unreferenced chunks, so a writer that found a chunk
// present and skipped the insert could have it removed before its envelope
// commits, leaving a dangling reference.
//
// The conflict guard compares plain_bytes only, and that is deliberate: the
// compressed payload is not comparable because the codec is not part of a
// chunk's identity and may change between writers. So this is a length check,
// not a proof that the stored bytes are the bytes this digest names. That proof
// happens on read, where the reassembled body is verified against its plaintext
// digest and a mismatch fails the whole read.
func putEvidenceChunk(
	ctx context.Context,
	transaction *sql.Tx,
	digest [sha256.Size]byte,
	chunk []byte,
) error {
	codec := chunkCodecZstd
	stored := bodyEncoder.EncodeAll(chunk, nil)
	if len(stored) >= len(chunk) {
		codec = chunkCodecIdentity
		stored = chunk
	}
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO runtime_evidence_chunks(digest, plain_bytes, codec, payload)
		 VALUES (?, ?, ?, ?) ON CONFLICT(digest) DO UPDATE
		 SET digest = excluded.digest
		 WHERE runtime_evidence_chunks.plain_bytes = excluded.plain_bytes`,
		digest[:], len(chunk), codec, stored,
	)
	if err != nil {
		return fmt.Errorf("persist evidence chunk: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errors.New("stored evidence chunk disagrees with its digest")
	}
	return nil
}

// loadEvidenceBody rebuilds a body from its manifest and verifies the result
// against the digest it was stored under. A mismatch is an error, never a
// partial body.
type evidenceQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadEvidenceBody(
	ctx context.Context,
	queryer evidenceQueryer,
	digest []byte,
) ([]byte, error) {
	if len(digest) == 0 {
		return nil, nil
	}
	var plainBytes int
	var manifest []byte
	if err := queryer.QueryRowContext(
		ctx,
		`SELECT plain_bytes, chunk_manifest FROM runtime_evidence_bodies
		  WHERE digest = ?`,
		digest,
	).Scan(&plainBytes, &manifest); err != nil {
		return nil, fmt.Errorf("read evidence body: %w", err)
	}
	if len(manifest)%sha256.Size != 0 || len(manifest) == 0 ||
		plainBytes > rawevidence.DefaultMaximumBodyBytes {
		return nil, errors.New("stored evidence body manifest is invalid")
	}
	// One query for every chunk, not one per chunk: a 16 MiB body reaches the
	// 8,192-chunk bound, and the store keeps a single connection.
	chunks, err := loadEvidenceChunks(ctx, queryer, manifest)
	if err != nil {
		return nil, err
	}
	body := make([]byte, 0, plainBytes)
	for offset := 0; offset < len(manifest); offset += sha256.Size {
		chunk, ok := chunks[string(manifest[offset:offset+sha256.Size])]
		if !ok {
			return nil, errors.New("stored evidence body names a missing chunk")
		}
		body = append(body, chunk...)
	}
	if len(body) != plainBytes {
		return nil, errors.New("reassembled evidence body has the wrong length")
	}
	rebuilt := sha256.Sum256(body)
	if !equalDigest(rebuilt[:], digest) {
		return nil, errors.New("reassembled evidence body failed its digest")
	}
	return body, nil
}

func loadEvidenceChunks(
	ctx context.Context,
	queryer evidenceQueryer,
	manifest []byte,
) (map[string][]byte, error) {
	wanted := make([]string, 0, len(manifest)/sha256.Size)
	for offset := 0; offset < len(manifest); offset += sha256.Size {
		wanted = append(
			wanted,
			strings.ToUpper(hex.EncodeToString(manifest[offset:offset+sha256.Size])),
		)
	}
	encoded, err := json.Marshal(uniqueStrings(wanted))
	if err != nil {
		return nil, errors.New("stored evidence body manifest is invalid")
	}
	rows, err := queryer.QueryContext(
		ctx,
		`SELECT chunks.digest, chunks.plain_bytes, chunks.codec, chunks.payload
		   FROM runtime_evidence_chunks AS chunks
		   JOIN json_each(?) AS wanted ON wanted.value = hex(chunks.digest)`,
		string(encoded),
	)
	if err != nil {
		return nil, fmt.Errorf("read evidence chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	chunks := make(map[string][]byte, len(wanted))
	for rows.Next() {
		var digest, stored []byte
		var plainBytes int
		var codec string
		if err := rows.Scan(&digest, &plainBytes, &codec, &stored); err != nil {
			return nil, fmt.Errorf("scan evidence chunk: %w", err)
		}
		if plainBytes > evidencechunk.MaximumBytes {
			return nil, errors.New("stored evidence chunk length is out of range")
		}
		switch codec {
		case chunkCodecIdentity:
		case chunkCodecZstd:
			decoded, decodeErr := bodyDecoder.DecodeAll(
				stored, make([]byte, 0, plainBytes),
			)
			if decodeErr != nil {
				return nil, fmt.Errorf("decompress evidence chunk: %w", decodeErr)
			}
			stored = decoded
		default:
			return nil, errors.New("stored evidence chunk codec is unsupported")
		}
		if len(stored) != plainBytes {
			return nil, errors.New("stored evidence chunk has the wrong length")
		}
		chunks[string(digest)] = stored
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evidence chunks: %w", err)
	}
	return chunks, nil
}

// purgeUnreferencedEvidenceBytes releases bodies and then chunks that nothing
// points at any more. Order matters: a chunk stays reachable while any body
// still names it.
func purgeUnreferencedEvidenceBytes(
	ctx context.Context,
	transaction *sql.Tx,
) error {
	if _, err := transaction.ExecContext(
		ctx,
		`DELETE FROM runtime_evidence_bodies
		  WHERE digest NOT IN (
		    SELECT stored_body_digest FROM runtime_raw_evidence_envelopes
		     WHERE stored_body_digest IS NOT NULL
		  )`,
	); err != nil {
		return fmt.Errorf("purge unreferenced evidence bodies: %w", err)
	}
	if _, err := transaction.ExecContext(
		ctx,
		// The recursion carries the body's key, not its manifest. Carrying the
		// manifest copies the whole BLOB into every generated row, so the sweep
		// materializes O(32*K^2) bytes for a K-chunk body; joining back by
		// primary key is linear. Measured at a 4 MiB body it is the difference
		// between 0.66 s and 0.42 s for the suite, and the gap widens with K
		// toward the 8,192-chunk bound this file documents.
		`WITH RECURSIVE spans(body_digest, position) AS (
		   SELECT digest, 1 FROM runtime_evidence_bodies
		   UNION ALL
		   SELECT spans.body_digest, spans.position + 32
		     FROM spans
		     JOIN runtime_evidence_bodies AS bodies
		       ON bodies.digest = spans.body_digest
		    WHERE spans.position + 32 <= length(bodies.chunk_manifest)
		 )
		 DELETE FROM runtime_evidence_chunks
		  WHERE digest NOT IN (
		    SELECT substr(bodies.chunk_manifest, spans.position, 32)
		      FROM spans
		      JOIN runtime_evidence_bodies AS bodies
		        ON bodies.digest = spans.body_digest
		  )`,
	); err != nil {
		return fmt.Errorf("purge unreferenced evidence chunks: %w", err)
	}
	return nil
}

func equalDigest(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
