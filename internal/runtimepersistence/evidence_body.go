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
	// EncodeAll remains safe for concurrent callers, but its expensive workspace
	// count is fixed rather than coupled to the machine's CPU count.
	encoder, err := zstd.NewWriter(
		nil,
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithEncoderConcurrency(1),
	)
	if err != nil {
		panic(fmt.Errorf("construct evidence body encoder: %w", err))
	}
	return encoder
}

func mustZstdReader() *zstd.Decoder {
	// Every DecodeAll caller supplies the validated retained plain-byte count
	// as capacity. A forged compressed frame must not inflate output beyond it.
	decoder, err := zstd.NewReader(nil, zstd.WithDecodeAllCapLimit(true), zstd.WithDecoderMaxMemory(64<<20))
	if err != nil {
		panic(fmt.Errorf("construct evidence body decoder: %w", err))
	}
	return decoder
}

// storeEvidenceBody writes the chunks of body and its manifest, returning the
// digest an envelope should reference. An empty body has no row and no digest.
// Deduplication never updates the primary key, even to itself: that would make
// SQLite revisit all retained references to shared bodies/chunks on each write.
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
		 VALUES (?, ?, ?) ON CONFLICT(digest) DO UPDATE SET plain_bytes = excluded.plain_bytes
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
		 SET plain_bytes = excluded.plain_bytes
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
	if len(digest) != sha256.Size {
		return nil, errors.New("stored evidence body digest is invalid")
	}
	var plainBytes, manifestBytes int
	var manifest []byte
	if err := queryer.QueryRowContext(
		ctx,
		// Guard the BLOB projection itself: checking len(manifest) after Scan
		// would already have materialized a corrupt oversized manifest.
		`SELECT plain_bytes, length(chunk_manifest),
		   CASE WHEN plain_bytes BETWEEN 1 AND ?
		     AND typeof(chunk_manifest)='blob'
		     AND length(chunk_manifest) BETWEEN 32 AND ?
		     AND length(chunk_manifest)%32=0 THEN chunk_manifest END
		 FROM runtime_evidence_bodies WHERE digest = ?`,
		rawevidence.DefaultMaximumBodyBytes, maximumChunkManifestBytes, digest,
	).Scan(&plainBytes, &manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("read evidence body: %w", err)
	}
	if plainBytes < 1 || plainBytes > rawevidence.DefaultMaximumBodyBytes ||
		manifestBytes < sha256.Size || manifestBytes > maximumChunkManifestBytes ||
		manifestBytes%sha256.Size != 0 || len(manifest) != manifestBytes {
		return nil, errors.New("stored evidence body manifest is invalid")
	}
	// Preflight all unique chunks and their manifest occurrence counts before
	// loading any payload. Both queries use indexed digest probes, not one
	// query per chunk or a scan of unrelated retained history.
	chunks, err := loadEvidenceChunks(ctx, queryer, manifest, plainBytes)
	if err != nil {
		return nil, err
	}
	body := make([]byte, 0, plainBytes)
	for offset := 0; offset < len(manifest); offset += sha256.Size {
		chunk, ok := chunks[string(manifest[offset:offset+sha256.Size])]
		if !ok {
			return nil, errors.New("stored evidence body names a missing chunk")
		}
		if len(chunk) > plainBytes-len(body) {
			return nil, errors.New("stored evidence body exceeds its declared length")
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

// evidenceChunkBounds is bounded scalar metadata copied before any payload
// read. The second query matches these exact values and projects NULL on a
// mismatch, keeping nontransactional Raw readers safe across concurrent writes.
type evidenceChunkBounds struct {
	Digest      string `json:"digest"`
	PlainBytes  int    `json:"plainBytes"`
	StoredBytes int    `json:"storedBytes"`
	Codec       string `json:"codec"`
}

func loadEvidenceChunkBounds(ctx context.Context, queryer evidenceQueryer, manifest []byte, plainBytes int) (map[string]evidenceChunkBounds, error) {
	occurrences := make(map[string]int)
	for offset := 0; offset < len(manifest); offset += sha256.Size {
		occurrences[string(manifest[offset:offset+sha256.Size])]++
	}
	wanted := make([]string, 0, len(occurrences))
	for digest := range occurrences {
		wanted = append(wanted, strings.ToUpper(hex.EncodeToString([]byte(digest))))
	}
	encoded, err := json.Marshal(wanted)
	if err != nil {
		return nil, errors.New("stored evidence body manifest is invalid")
	}
	rows, err := queryer.QueryContext(ctx,
		// Keep the stored BLOB primary key bare on BOTH passes. hex(chunks.digest)
		// would scan unrelated history for each requested manifest entry.
		// Codec is allowlisted in SQL as its TEXT column has no length CHECK.
		`SELECT chunks.digest, chunks.plain_bytes,
		   CASE WHEN chunks.codec IN ('identity','zstd') THEN chunks.codec ELSE '' END,
		   CASE WHEN typeof(chunks.payload)='blob' THEN length(chunks.payload) ELSE 0 END
		 FROM json_each(?) AS wanted
		 JOIN runtime_evidence_chunks AS chunks ON chunks.digest=unhex(wanted.value)`,
		string(encoded))
	if err != nil {
		return nil, fmt.Errorf("read evidence chunk bounds: %w", err)
	}
	// This helper returns only after closing rows: callers may have one SQL
	// connection and cannot start the payload query while these rows are open.
	defer rows.Close()
	bounds := make(map[string]evidenceChunkBounds, len(occurrences))
	total := 0
	for rows.Next() {
		var digest []byte
		var value evidenceChunkBounds
		if err := rows.Scan(&digest, &value.PlainBytes, &value.Codec, &value.StoredBytes); err != nil {
			return nil, fmt.Errorf("scan evidence chunk bounds: %w", err)
		}
		key := string(digest)
		count := occurrences[key]
		if len(digest) != sha256.Size || count == 0 {
			return nil, errors.New("stored evidence chunk identity is invalid")
		}
		if _, duplicate := bounds[key]; duplicate {
			return nil, errors.New("stored evidence chunk identity is duplicated")
		}
		if value.PlainBytes < 1 || value.PlainBytes > evidencechunk.MaximumBytes {
			return nil, errors.New("stored evidence chunk length is out of range")
		}
		// The writer stores identity encoding whenever compression is not
		// smaller. Thus no valid stored payload exceeds one maximum chunk.
		if value.StoredBytes < 1 || value.StoredBytes > evidencechunk.MaximumBytes {
			return nil, errors.New("stored evidence chunk payload length is out of range")
		}
		if value.Codec != chunkCodecIdentity && value.Codec != chunkCodecZstd {
			return nil, errors.New("stored evidence chunk codec is unsupported")
		}
		if value.Codec == chunkCodecIdentity && value.StoredBytes != value.PlainBytes {
			return nil, errors.New("stored identity chunk has the wrong length")
		}
		// Count repeated references, not just distinct chunks. Divide before
		// multiplying so even corrupt metadata cannot overflow the sum.
		if value.PlainBytes > (plainBytes-total)/count {
			return nil, errors.New("stored evidence manifest exceeds its declared body length")
		}
		total += value.PlainBytes * count
		value.Digest = strings.ToUpper(hex.EncodeToString(digest))
		bounds[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evidence chunk bounds: %w", err)
	}
	if len(bounds) != len(occurrences) || total != plainBytes {
		return nil, errors.New("stored evidence manifest does not match its declared body length")
	}
	return bounds, nil
}

func loadEvidenceChunks(ctx context.Context, queryer evidenceQueryer, manifest []byte, plainBytes int) (map[string][]byte, error) {
	bounds, err := loadEvidenceChunkBounds(ctx, queryer, manifest, plainBytes)
	if err != nil {
		return nil, err
	}
	wanted := make([]evidenceChunkBounds, 0, len(bounds))
	for _, value := range bounds {
		wanted = append(wanted, value)
	}
	encoded, err := json.Marshal(wanted)
	if err != nil {
		return nil, errors.New("stored evidence chunk bounds are invalid")
	}
	rows, err := queryer.QueryContext(ctx,
		// Checking after Scan is too late for an oversized payload. These
		// expected lengths/codecs have already passed the metadata preflight;
		// CASE also guards a mutation between queries for non-Tx callers.
		`SELECT chunks.digest,
		   CASE WHEN chunks.plain_bytes=json_extract(wanted.value,'$.plainBytes')
		     AND chunks.codec=json_extract(wanted.value,'$.codec')
		     AND typeof(chunks.payload)='blob'
		     AND length(chunks.payload)=json_extract(wanted.value,'$.storedBytes')
		   THEN chunks.payload END
		 FROM json_each(?) AS wanted
		 JOIN runtime_evidence_chunks AS chunks
		   ON chunks.digest=unhex(json_extract(wanted.value,'$.digest'))`,
		string(encoded))
	if err != nil {
		return nil, fmt.Errorf("read evidence chunks: %w", err)
	}
	defer rows.Close()
	chunks := make(map[string][]byte, len(bounds))
	decodedBytes := 0
	for rows.Next() {
		var digest, stored []byte
		if err := rows.Scan(&digest, &stored); err != nil {
			return nil, fmt.Errorf("scan evidence chunk: %w", err)
		}
		key := string(digest)
		value, found := bounds[key]
		if !found || len(stored) != value.StoredBytes {
			return nil, errors.New("stored evidence chunk changed after bounds validation")
		}
		if _, duplicate := chunks[key]; duplicate || value.PlainBytes > plainBytes-decodedBytes {
			return nil, errors.New("stored evidence chunks exceed their declared body length")
		}
		if value.Codec == chunkCodecZstd {
			// The declared length was checked before payload materialization,
			// and DecodeAll's cap limit prevents output growth beyond it.
			decoded, decodeErr := bodyDecoder.DecodeAll(stored, make([]byte, 0, value.PlainBytes))
			if decodeErr != nil {
				return nil, fmt.Errorf("decompress evidence chunk: %w", decodeErr)
			}
			stored = decoded
		}
		if len(stored) != value.PlainBytes {
			return nil, errors.New("stored evidence chunk has the wrong length")
		}
		decodedBytes += len(stored)
		chunks[key] = stored
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evidence chunks: %w", err)
	}
	if len(chunks) != len(bounds) {
		return nil, errors.New("stored evidence body names a missing chunk")
	}
	return chunks, nil
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
