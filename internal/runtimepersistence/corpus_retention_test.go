package runtimepersistence

import (
	"crypto/sha256"
	"database/sql"
	"os"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/vibe-agi/vibermate/internal/evidencechunk"
)

// TestRecordedCorpusRetentionMatchesItsProjection measures what the raw plane
// would cost for a real capture corpus, using the production chunker rather than
// an estimate. It is the only way to check the plan's raw-plane figure against
// something other than a simulation.
//
// It is opt-in because it needs a populated database from an earlier schema,
// which no CI checkout has. Point VIBERMATE_CORPUS_DB at a read-only copy:
//
//	VIBERMATE_CORPUS_DB=/tmp/runtime-copy.db go test -v -run \
//	  TestRecordedCorpusRetentionMatchesItsProjection ./internal/runtimepersistence
//
// Request bodies are reconstructed from the stored transcripts because the
// corpus predates this plan and its payloads are still sealed. The
// reconstruction omits tool schemas and the system parameter, which are the most
// repetitive parts of a real body, so the ratio it reports is a floor.
func TestRecordedCorpusRetentionMatchesItsProjection(t *testing.T) {
	corpus := os.Getenv("VIBERMATE_CORPUS_DB")
	if corpus == "" {
		t.Skip("set VIBERMATE_CORPUS_DB to a read-only corpus copy")
	}
	database, err := sql.Open("sqlite", corpus)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	payloads := map[string][]byte{}
	rows, err := database.Query(
		`SELECT digest, payload_json FROM runtime_exchange_content_messages`,
	)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var digest string
		var payload []byte
		if err := rows.Scan(&digest, &payload); err != nil {
			t.Fatal(err)
		}
		payloads[digest] = payload
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()

	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()

	roots, err := database.Query(
		`SELECT request_transcript_digest FROM runtime_exchange_contents`,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = roots.Close() }()

	stored := map[[sha256.Size]byte]int{}
	var reconstructed, manifestBytes int64
	exchanges := 0
	for roots.Next() {
		var root string
		if err := roots.Scan(&root); err != nil {
			t.Fatal(err)
		}
		body := reconstructCorpusBody(t, database, root, payloads)
		if len(body) == 0 {
			continue
		}
		exchanges++
		reconstructed += int64(len(body))
		for _, chunk := range evidencechunk.Split(body) {
			manifestBytes += sha256.Size
			digest := sha256.Sum256(chunk)
			if _, seen := stored[digest]; seen {
				continue
			}
			stored[digest] = len(encoder.EncodeAll(chunk, nil))
		}
	}
	if err := roots.Err(); err != nil {
		t.Fatal(err)
	}
	if exchanges == 0 {
		t.Fatal("corpus produced no reconstructable request bodies")
	}

	var chunkBytes int64
	for _, size := range stored {
		chunkBytes += int64(size)
	}
	total := chunkBytes + manifestBytes
	t.Logf(
		"exchanges=%d reconstructed=%.1f MB chunks=%d stored=%.2f MB "+
			"manifests=%.2f MB total=%.2f MB ratio=%.1fx",
		exchanges,
		float64(reconstructed)/(1<<20),
		len(stored),
		float64(chunkBytes)/(1<<20),
		float64(manifestBytes)/(1<<20),
		float64(total)/(1<<20),
		float64(reconstructed)/float64(total),
	)
	if total >= reconstructed {
		t.Fatalf(
			"chunked storage (%d bytes) did not reduce the corpus (%d bytes)",
			total, reconstructed,
		)
	}
}

func reconstructCorpusBody(
	t *testing.T,
	database *sql.DB,
	root string,
	payloads map[string][]byte,
) []byte {
	t.Helper()
	rows, err := database.Query(
		`WITH RECURSIVE chain(digest, parent_digest, message_digest, depth) AS (
		   SELECT digest, parent_digest, message_digest, depth
		     FROM runtime_exchange_content_transcripts WHERE digest = ?
		   UNION ALL
		   SELECT nodes.digest, nodes.parent_digest, nodes.message_digest, nodes.depth
		     FROM runtime_exchange_content_transcripts AS nodes
		     JOIN chain ON nodes.digest = chain.parent_digest
		 )
		 SELECT message_digest FROM chain ORDER BY depth ASC`,
		root,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	body := []byte(`{"messages":[`)
	first := true
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			t.Fatal(err)
		}
		payload, ok := payloads[digest]
		if !ok {
			continue
		}
		if !first {
			body = append(body, ',')
		}
		first = false
		body = append(body, payload...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if first {
		return nil
	}
	return append(body, ']', '}')
}
