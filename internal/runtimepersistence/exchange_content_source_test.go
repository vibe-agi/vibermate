package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func sourceOnlyRecord(t *testing.T, id string, blocks ...exchangecontent.Block) exchangecontent.Record {
	t.Helper()
	r := contentRecordFixture(t, id, time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC))
	r.Request.System = nil
	r.Request.Messages = []exchangecontent.Message{{Role: "user", Blocks: blocks}}
	r.Response = nil
	return r
}

func sourceMixedLegacyDigest(t *testing.T, blocks []exchangecontent.Block) string {
	t.Helper()
	h := sha256.New()
	io.WriteString(h, `{"role":"user","blocks":[`)
	chunk := strings.Repeat(`\u0026`, 4096)
	for i, b := range blocks {
		if i > 0 {
			io.WriteString(h, ",")
		}
		if len(b.Text) == 6<<20 && b.Text[0] == '&' {
			io.WriteString(h, `{"kind":"text","availability":"recorded","text":"`)
			for n := 0; n < 6<<20; n += 4096 {
				io.WriteString(h, chunk)
			}
			io.WriteString(h, `","originalSize":6291456}`)
		} else {
			data, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			h.Write(data)
		}
	}
	io.WriteString(h, `]}`)
	return hex.EncodeToString(h.Sum(nil))
}

func TestContentSourceMixedRepeatedFramesRejectCorruption(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	large := sourceText(strings.Repeat("&", 6<<20))
	blocks := []exchangecontent.Block{sourceText("before"), large, sourceText("after"), large}
	r := sourceOnlyRecord(t, "mixed-frames", blocks...)
	r.Request.Messages = append([]exchangecontent.Message{{Role: "user", Blocks: []exchangecontent.Block{sourceText("earlier message")}}}, r.Request.Messages...)
	if err := s.exchangeContents.Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	var digest, manifest string
	if err := s.database.QueryRow(`SELECT m.digest,m.block_manifest FROM runtime_exchange_content_messages m JOIN runtime_exchange_content_transcripts n ON n.message_digest=m.digest JOIN runtime_exchange_contents c ON c.request_transcript_digest=n.digest WHERE c.exchange_id=?`, r.ExchangeID).Scan(&digest, &manifest); err != nil {
		t.Fatal(err)
	}
	if digest != sourceMixedLegacyDigest(t, blocks) || len(manifest) != 6*64 || manifest[64:192] != manifest[256:384] {
		t.Fatal("logical hash or ordered repeated groups changed")
	}
	refs := map[string]bool{}
	rows, err := s.database.Query(`SELECT block_digest FROM runtime_exchange_content_block_refs WHERE message_digest=?`, digest)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		refs[d] = true
	}
	rows.Close()
	wantRefs := map[string]bool{}
	for i := 0; i < len(manifest); i += 64 {
		wantRefs[manifest[i:i+64]] = true
	}
	if !reflect.DeepEqual(refs, wantRefs) || len(refs) != 4 {
		t.Fatalf("trigger refs=%d want=%d", len(refs), len(wantRefs))
	}
	got, err := s.exchangeContents.Get(ctx, r.ExchangeID, r.RecordedAt)
	if err != nil || len(got.Request.Messages[1].Blocks) != 4 || got.Request.Messages[1].Blocks[3].Text != large.Text {
		t.Fatalf("mixed groups: %v", err)
	}
	ref, err := loadStoredContentReference(ctx, s.reads, r.ExchangeID, r.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	cursor := pageCursor(ref, "request", "body", 2)
	firstDigest, lastDigest := manifest[64:128], manifest[128:192]
	plainBytes, codec, stored, err := s.exchangeContents.physicalContentRow(ctx, firstDigest)
	if err != nil {
		t.Fatal(err)
	}
	first, err := decodeStoredPhysicalPayload(firstDigest, plainBytes, codec, stored)
	if err != nil {
		t.Fatal(err)
	}
	assertRejected := func(t *testing.T) {
		t.Helper()
		got, err := s.exchangeContents.Get(ctx, r.ExchangeID, r.RecordedAt)
		if !errors.Is(err, exchangecontent.ErrInvalidEvidence) || !reflect.DeepEqual(got, exchangecontent.Record{}) {
			t.Fatalf("full/batch returned partial content: %v", err)
		}
		p, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, encodeContentCursor(cursor))
		if !errors.Is(err, exchangecontent.ErrInvalidEvidence) || !reflect.DeepEqual(p, exchangecontent.ContentPage{}) {
			t.Fatalf("later corruption disclosed earlier selected body: %v", err)
		}
	}
	for _, name := range []string{"old-physical-sha", "wrong-length", "ordinal", "count", "total", "logical-sha", "magic", "truncated-header", "nested-body", "reordered", "repeated-ordinal", "mixed-ordinary", "missing-database-row", "unknown-codec", "trailing-frame", "final-canonical-byte", "group-order", "oversized-stored-payload"} {
		t.Run(name, func(t *testing.T) {
			var inserted string
			t.Cleanup(func() {
				if _, err := s.database.Exec(`UPDATE runtime_exchange_content_messages SET block_manifest=? WHERE digest=?`, manifest, digest); err != nil {
					t.Fatal(err)
				}
				if _, err := s.database.Exec(`INSERT INTO runtime_exchange_content_blocks(digest,plain_bytes,codec,payload) VALUES(?,?,?,?) ON CONFLICT(digest) DO UPDATE SET plain_bytes=excluded.plain_bytes,codec=excluded.codec,payload=excluded.payload`, firstDigest, plainBytes, codec, stored); err != nil {
					t.Fatal(err)
				}
				if inserted != "" {
					if _, err := s.database.Exec(`DELETE FROM runtime_exchange_content_blocks WHERE digest=?`, inserted); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.database.Exec(`PRAGMA foreign_keys=ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := s.database.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
					t.Fatal(err)
				}
			})
			changed := append([]byte(nil), first...)
			newManifest := manifest
			switch name {
			case "old-physical-sha":
				changed[len(changed)-1] ^= 1
				if _, err := s.database.Exec(`UPDATE runtime_exchange_content_blocks SET codec='identity',payload=? WHERE digest=?`, changed, firstDigest); err != nil {
					t.Fatal(err)
				}
			case "wrong-length":
				if _, err := s.database.Exec(`UPDATE runtime_exchange_content_blocks SET plain_bytes=plain_bytes-1 WHERE digest=?`, firstDigest); err != nil {
					t.Fatal(err)
				}
			case "reordered":
				newManifest = manifest[:64] + lastDigest + firstDigest + manifest[192:]
			case "repeated-ordinal":
				newManifest = manifest[:128] + firstDigest + manifest[192:]
			case "mixed-ordinary":
				newManifest = manifest[:128] + manifest[:64] + manifest[192:]
			case "group-order":
				newManifest = manifest[192:256] + manifest[64:192] + manifest[:64] + manifest[256:]
			case "oversized-stored-payload":
				if _, err := s.database.Exec(`UPDATE runtime_exchange_content_blocks SET payload=? WHERE digest=?`, make([]byte, exchangecontent.MaxEncodedBytes+1), firstDigest); err != nil {
					t.Fatal(err)
				}
			case "missing-database-row":
				if _, err := s.database.Exec(`DELETE FROM runtime_exchange_content_blocks WHERE digest=?`, firstDigest); err == nil {
					t.Fatal("FK failed to protect live frame")
				}
				if _, err := s.database.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				if _, err := s.database.Exec(`DELETE FROM runtime_exchange_content_blocks WHERE digest=?`, firstDigest); err != nil {
					t.Fatal(err)
				}
			case "unknown-codec":
				if _, err := s.database.Exec(`UPDATE runtime_exchange_content_blocks SET codec='unknown' WHERE digest=?`, firstDigest); err == nil {
					t.Fatal("codec CHECK failed")
				}
				if _, err := s.database.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := s.database.Exec(`UPDATE runtime_exchange_content_blocks SET codec='unknown' WHERE digest=?`, firstDigest); err != nil {
					t.Fatal(err)
				}
			default:
				if name == "trailing-frame" || name == "final-canonical-byte" {
					n, c, data, err := s.exchangeContents.physicalContentRow(ctx, lastDigest)
					if err != nil {
						t.Fatal(err)
					}
					changed, err = decodeStoredPhysicalPayload(lastDigest, n, c, data)
					if err != nil {
						t.Fatal(err)
					}
					if name == "trailing-frame" {
						changed = append(changed, ' ')
					} else {
						changed[len(changed)-1] ^= 1
					}
				}
				switch name {
				case "ordinal":
					binary.BigEndian.PutUint32(changed[48:52], 1)
				case "count":
					binary.BigEndian.PutUint32(changed[52:56], 3)
				case "total":
					binary.BigEndian.PutUint64(changed[40:48], binary.BigEndian.Uint64(changed[40:48])+1)
				case "logical-sha":
					changed[8] ^= 1
				case "magic":
					changed[0] ^= 1
				case "truncated-header":
					changed = changed[:40]
				case "nested-body":
					copy(changed[56:], storedFrameMagic)
				}
				sum := sha256.Sum256(changed)
				inserted = hex.EncodeToString(sum[:])
				if _, err := s.database.Exec(`INSERT INTO runtime_exchange_content_blocks(digest,plain_bytes,codec,payload) VALUES(?,?,'identity',?)`, inserted, len(changed), changed); err != nil {
					t.Fatal(err)
				}
				newManifest = manifest[:64] + inserted + manifest[128:]
				if name == "trailing-frame" || name == "final-canonical-byte" {
					newManifest = manifest[:128] + inserted + manifest[192:]
				}
			}
			if _, err := s.database.Exec(`UPDATE runtime_exchange_content_messages SET block_manifest=? WHERE digest=?`, newManifest, digest); err != nil {
				t.Fatalf("reachable fixture mutation failed: %v", err)
			}
			assertRejected(t)
		})
	}
	sourceDatabaseIntegrity(t, s)
}

func TestContentSourceRealResponseEveryBodyPage(t *testing.T) {
	bodyBytes := 6 << 20
	if testing.Short() {
		// Stay larger than the inline projection window and validate every byte
		// of five real body pages. CI separately races the original full matrix.
		bodyBytes = 5 * exchangecontent.PageBodyBytes
	}
	wantPages := (bodyBytes + exchangecontent.PageBodyBytes - 1) / exchangecontent.PageBodyBytes
	if (testing.Short() && wantPages < 3) || (!testing.Short() && (bodyBytes != 6<<20 || wantPages != 192)) {
		t.Fatal("short/full response fixture contract changed")
	}
	t.Logf("response fixture bytes=%d pages=%d exhaustive=true", bodyBytes, wantPages)
	testContentSourceRealResponsePages(t, bodyBytes, []string{"&", "x"}, true)
}

func TestContentSourceRealResponseFragmentBoundary(t *testing.T) {
	if !testing.Short() {
		t.Skip("full-size initial, continuation, boundary and final pages are covered by the exhaustive matrix")
	}
	// Keep the actual six-MiB ampersand value that crosses the physical frame
	// boundary, with full readback and selected pages rather than 192 rereads.
	testContentSourceRealResponsePages(t, 6<<20, []string{"&"}, false)
}

func testContentSourceRealResponsePages(t *testing.T, bodyBytes int, characters []string, everyPage bool) {
	t.Helper()
	ctx := context.Background()
	l := candidateContentLimits()
	// Exact existing Source phase for an agent-free ordinary text:
	// n + 4*F(n) + 4096, F's four sanitizer expansion stages. This is a
	// synthetic fixture reservation, not a production capacity selection.
	out := uint64(bodyBytes)
	for _, rule := range [4][2]uint64{{8, 11}, {15, 2}, {15, 6}, {11, 7}} {
		out += (out / rule[0]) * rule[1]
	}
	l.Scratch.PayloadBytes = uint64(bodyBytes) + 4*out + 4096
	l.Scratch.StructureBytes = uint64(reflect.TypeFor[exchangecontent.Block]().Size()) + 4096
	if bodyBytes == 6<<20 && l.Scratch.PayloadBytes != 161477116 {
		t.Fatal("six-MiB fixture scratch derivation changed")
	}
	codec, err := openairesponses.New(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request := protocolcore.Request{RequestedModel: "model", EffectiveModel: "model", MaxOutputTokens: 16, Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "question"}}}}}
	base := contentRecordFixture(t, "real-source", time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC))
	for _, ch := range characters {
		wire := []byte(`{"id":"response","status":"completed","model":"model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + strings.Repeat(ch, bodyBytes) + `"}]}]}`)
		response, _, err := codec.DecodeProviderResponse(request, wire)
		if err != nil {
			t.Fatal(err)
		}
		small := l
		small.Scratch.PayloadBytes--
		if _, err := exchangecontent.NewSourceWithin(small, base.ExchangeID, base.Frozen, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, base.RecordedAt, request, &response); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
			t.Fatalf("too-small actual Source phase was accepted: %v", err)
		}
		for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingFull, environment.ContentRecordingMetadataOnly, environment.ContentRecordingOff} {
			t.Run(ch+"/"+string(mode), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "runtime.db")
				s := openCandidateContentStore(t, path, &l)
				policy := environment.ContentRecordingPolicy{Mode: mode, RetentionDays: 1}
				pending, err := exchangecontent.NewSourceWithin(l, base.ExchangeID, base.Frozen, policy, base.RecordedAt, request, nil)
				if mode == environment.ContentRecordingOff {
					if !errors.Is(err, exchangecontent.ErrInvalidEvidence) || pending != nil || countRows(t, s, "runtime_exchange_contents") != 0 {
						t.Fatal("Off published evidence")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := putCandidateSource(t, s, pending); err != nil {
					t.Fatal(err)
				}
				complete, err := exchangecontent.NewSourceWithin(l, base.ExchangeID, base.Frozen, policy, base.RecordedAt.Add(time.Second), request, &response)
				if err != nil {
					t.Fatal(err)
				}
				if err := putCandidateSource(t, s, complete); err != nil {
					t.Fatal(err)
				}
				shutdownTestStore(t, s)
				s = openCandidateContentStore(t, path, &l)
				got, err := s.exchangeContents.Get(ctx, base.ExchangeID, base.RecordedAt.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				if !got.RecordedAt.Equal(base.RecordedAt) || got.Response == nil || len(got.Response.Blocks) != 1 || got.Response.Blocks[0].OriginalSize != bodyBytes {
					t.Fatal("completion/reopen lost response metadata")
				}
				if mode == environment.ContentRecordingMetadataOnly {
					if got.Response.Blocks[0].Text != "" || got.Response.Blocks[0].Availability != exchangecontent.AvailabilityOmitted {
						t.Fatal("metadata mode retained body")
					}
					return
				}
				if got.Response.Blocks[0].Text != strings.Repeat(ch, bodyBytes) {
					t.Fatal("complete response changed")
				}
				projection, err := s.exchangeContents.GetPagedProjection(ctx, base.ExchangeID, base.RecordedAt, exchangecontent.RequestViewFull)
				if err != nil {
					t.Fatal(err)
				}
				message, err := s.exchangeContents.GetContentPage(ctx, base.ExchangeID, base.RecordedAt, projection.Response.Blocks[0].Deferred.Cursor)
				if err != nil {
					t.Fatal(err)
				}
				if !everyPage {
					ref, err := loadStoredContentReference(ctx, s.reads, base.ExchangeID, base.RecordedAt)
					if err != nil {
						t.Fatal(err)
					}
					var manifest string
					if err := s.database.QueryRow(`SELECT block_manifest FROM runtime_exchange_content_messages WHERE digest=?`, ref.responseDigest.String).Scan(&manifest); err != nil || len(manifest) != 2*storedDigestHexBytes {
						t.Fatalf("large ampersand response must span two physical frames: %v", err)
					}
					bodyCursor, err := decodeContentCursor(message.Blocks[0].Deferred.Cursor)
					if err != nil {
						t.Fatal(err)
					}
					// Six canonical bytes per ampersand put this page across the
					// actual first physical frame seam, including a split escape.
					seam := (storedFrameBody - len(`{"kind":"text","availability":"recorded","text":"`)) / 6
					for _, offset := range []int{0, exchangecontent.PageBodyBytes, seam - 1, bodyBytes - exchangecontent.PageBodyBytes} {
						bodyCursor.Offset = offset
						page, err := s.exchangeContents.GetContentPage(ctx, base.ExchangeID, base.RecordedAt, encodeContentCursor(bodyCursor))
						if err != nil || page.ExchangeID != base.ExchangeID || page.Kind != "text" || page.Offset != offset || page.Total != bodyBytes || page.Text != strings.Repeat(ch, exchangecontent.PageBodyBytes) {
							t.Fatalf("fragment page offset=%d: %v", offset, err)
						}
						if offset+len(page.Text) == bodyBytes {
							if page.NextCursor != "" {
								t.Fatal("final fragment page retained continuation")
							}
						} else {
							next, err := decodeContentCursor(page.NextCursor)
							want := bodyCursor
							want.Kind = "text"
							want.Offset += len(page.Text)
							if err != nil || next != want {
								t.Fatalf("fragment page continuation identity changed: %v", err)
							}
							bodyCursor = next
						}
					}
					return
				}
				cursor, offset, pages := message.Blocks[0].Deferred.Cursor, 0, 0
				for cursor != "" {
					page, err := s.exchangeContents.GetContentPage(ctx, base.ExchangeID, base.RecordedAt, cursor)
					if err != nil || page.Kind != "text" || page.Offset != offset || page.Total != bodyBytes || page.Text != strings.Repeat(ch, len(page.Text)) || len(page.Text) == 0 {
						t.Fatalf("page %d offset%d: %v", pages, offset, err)
					}
					offset += len(page.Text)
					pages++
					cursor = page.NextCursor
				}
				if offset != bodyBytes || pages != (bodyBytes+exchangecontent.PageBodyBytes-1)/exchangecontent.PageBodyBytes {
					t.Fatalf("reconstruction bytes=%d pages=%d", offset, pages)
				}
			})
		}
	}
}

func TestContentSourceAgentLegacyHashesAndStrictForms(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	for i, a := range []*exchangecontent.AgentContext{nil, {AgentName: "small", Author: "author", Recipient: "recipient"}, {AgentName: strings.Repeat("&", 512), Author: strings.Repeat("\"\\", 256), Recipient: "a" + strings.Repeat("\u2028", 170) + "a"}} {
		r := sourceOnlyRecord(t, fmt.Sprintf("agent-%d", i), sourceText("body"))
		r.Request.Messages[0].Agent = a
		legacy, err := json.Marshal(r.Request.Messages[0])
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(legacy)
		want := hex.EncodeToString(sum[:])
		if err := s.exchangeContents.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
		var digest string
		var stored []byte
		if err := s.database.QueryRow(`SELECT m.digest,m.agent_json FROM runtime_exchange_content_messages m JOIN runtime_exchange_content_transcripts n ON n.message_digest=m.digest JOIN runtime_exchange_contents c ON c.request_transcript_digest=n.digest WHERE c.exchange_id=?`, r.ExchangeID).Scan(&digest, &stored); err != nil || digest != want {
			t.Fatalf("legacy SHA changed: %s/%s %v", digest, want, err)
		}
		if a != nil {
			wantAgent, _ := json.Marshal(a)
			if len(wantAgent) > 4096 {
				var b bytes.Buffer
				e := json.NewEncoder(&b)
				e.SetEscapeHTML(false)
				if err := e.Encode(a); err != nil {
					t.Fatal(err)
				}
				wantAgent = bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
			}
			if !bytes.Equal(stored, wantAgent) || len(stored) > 4096 {
				t.Fatal("physical agent representation changed")
			}
		}
		if _, err := s.exchangeContents.Get(ctx, r.ExchangeID, r.RecordedAt); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			for _, invalid := range []string{`{"agentName":"small","agentName":"small","author":"author","recipient":"recipient"}`, `{"agentName":"small","author":"author","recipient":"recipient","unknown":1}`, `{"agentName":"small","author":"author","recipient":"recipient"} `, `{"author":"author","agentName":"small","recipient":"recipient"}`} {
				if _, err := s.database.Exec(`UPDATE runtime_exchange_content_messages SET agent_json=? WHERE digest=?`, []byte(invalid), digest); err != nil {
					t.Fatalf("reachable agent mutation blocked: %v", err)
				}
				if _, err := s.exchangeContents.Get(ctx, r.ExchangeID, r.RecordedAt); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
					t.Fatalf("accepted noncanonical agent: %v", err)
				}
			}
			if _, err := s.database.Exec(`UPDATE runtime_exchange_content_messages SET agent_json=? WHERE digest=?`, stored, digest); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestContentSourceExactRawHTMLEscapeBudget(t *testing.T) {
	l := candidateContentLimits()
	r := sourceOnlyRecord(t, "raw-exact", exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "id", ToolName: "fn", Arguments: json.RawMessage(`{"value":"&"}`)})
	source, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := source.Measure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	l.RetainedBytes, l.StructureBytes, l.CanonicalBytes = cost.RetainedBytes, cost.StructureBytes, cost.CanonicalBytes
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	if err := putCandidateSource(t, s, source); err != nil {
		t.Fatal(err)
	}
	got, err := s.exchangeContents.Get(context.Background(), r.ExchangeID, r.RecordedAt)
	if err != nil {
		t.Fatalf("accepted exact Source raw bytes=%d retained=%d cannot read canonical HTML expansion: %v", len(r.Request.Messages[0].Blocks[0].Arguments), cost.RetainedBytes, err)
	}
	var value struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(got.Request.Messages[0].Blocks[0].Arguments, &value); err != nil || value.Value != "&" {
		t.Fatalf("raw value changed: %+v %v", value, err)
	}
}

func sourceSchemaHash(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.database.Query(`SELECT type,name,tbl_name,coalesce(sql,'') FROM sqlite_schema ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		var kind, name, table, sql string
		if err := rows.Scan(&kind, &name, &table, &sql); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", kind, name, table, sql)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func sourceDatabaseIntegrity(t *testing.T, s *Store) {
	t.Helper()
	rows, err := s.database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("foreign_key_check returned violations")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var result string
	if err := s.database.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil || result != "ok" {
		t.Fatalf("integrity_check=%s %v", result, err)
	}
	var orphan int
	if err := s.database.QueryRow(`SELECT count(*) FROM runtime_exchange_content_blocks b WHERE NOT EXISTS(SELECT 1 FROM runtime_exchange_content_block_refs r WHERE r.block_digest=b.digest)`).Scan(&orphan); err != nil || orphan != 0 {
		t.Fatalf("unowned physical rows=%d %v", orphan, err)
	}
}

func TestContentSourceRollbackRowsAndPreservesSharedData(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	for _, codec := range []string{chunkCodecZstd, chunkCodecIdentity} {
		t.Run(codec, func(t *testing.T) {
			s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
			schema := sourceSchemaHash(t, s)
			shared := sourceOnlyRecord(t, "shared-base", sourceText("shared"))
			large := sourceText(strings.Repeat("&", 11<<20))
			var frames []string
			if err := writeStoredBlockRows(ctx, large, l.CanonicalBytes, func(d string, _ []byte) error { frames = append(frames, d); return nil }); err != nil || len(frames) != 3 {
				t.Fatalf("three-frame fixture: %d %v", len(frames), err)
			}
			if codec == chunkCodecIdentity {
				shared.Request.Messages[0].Blocks = []exchangecontent.Block{large}
			}
			if err := s.exchangeContents.Put(ctx, shared); err != nil {
				t.Fatal(err)
			}
			if codec == chunkCodecIdentity {
				for _, digest := range frames {
					n, codec, data, err := s.exchangeContents.physicalContentRow(ctx, digest)
					if err != nil {
						t.Fatal(err)
					}
					plain, err := decodeStoredPhysicalPayload(digest, n, codec, data)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.database.Exec(`UPDATE runtime_exchange_content_blocks SET codec='identity',payload=? WHERE digest=?`, plain, digest); err != nil {
						t.Fatal(err)
					}
				}
			}
			baseline := countRows(t, s, "runtime_exchange_content_blocks")
			r := sourceOnlyRecord(t, "rollback-new", sourceText("new-before-frame"), large)
			for i, digest := range frames {
				event := "INSERT"
				if codec == chunkCodecIdentity {
					event = "UPDATE"
				}
				if _, err := s.database.Exec(fmt.Sprintf(`CREATE TRIGGER task4c_fault AFTER %s ON runtime_exchange_content_blocks WHEN NEW.digest='%s' BEGIN SELECT RAISE(ABORT,'task4c injected row fault'); END`, event, digest)); err != nil {
					t.Fatal(err)
				}
				if err := s.exchangeContents.Put(ctx, r); err == nil {
					t.Fatalf("row%d fault published content", i)
				}
				if _, err := s.database.Exec(`DROP TRIGGER task4c_fault`); err != nil {
					t.Fatal(err)
				}
				if countRows(t, s, "runtime_exchange_contents") != 1 || countRows(t, s, "runtime_exchange_content_blocks") != baseline {
					t.Fatalf("row%d left partial content/physical rows", i)
				}
				if _, err := s.exchangeContents.Get(ctx, shared.ExchangeID, shared.RecordedAt); err != nil {
					t.Fatalf("row%d damaged shared data: %v", i, err)
				}
				sourceDatabaseIntegrity(t, s)
			}
			if sourceSchemaHash(t, s) != schema {
				t.Fatal("Source rollback changed sqlite_schema")
			}
		})
	}
}

func TestContentSourceFramedOwnershipExpiryReopenAndCaptureDelete(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	path := filepath.Join(t.TempDir(), "runtime.db")
	s := openCandidateContentStore(t, path, &l)
	schema := sourceSchemaHash(t, s)
	parent := sourceOnlyRecord(t, "framed-parent", sourceText("question"))
	parent.Parent = exchangecontent.ParentRef{CaptureRunID: "framed-chain"}
	parent.Response = contentRecordFixture(t, "template", parent.RecordedAt).Response
	parent.Response.Blocks = []exchangecontent.Block{sourceText(strings.Repeat("&", 6<<20))}
	parent.Request.System = parent.Response.Blocks
	parent.ExpiresAt = parent.RecordedAt.Add(time.Hour)
	if err := s.exchangeContents.Put(ctx, parent); err != nil {
		t.Fatal(err)
	}
	child := sourceOnlyRecord(t, "framed-child", sourceText("next"))
	child.Parent = parent.Parent
	child.Request.Messages = []exchangecontent.Message{parent.Request.Messages[0], {Role: "assistant", Blocks: parent.Response.Blocks}, child.Request.Messages[0]}
	child.ExpiresAt = parent.ExpiresAt.Add(time.Hour)
	if err := s.exchangeContents.Put(ctx, child); err != nil {
		t.Fatal(err)
	}
	if n, err := s.exchangeContents.PurgeExpired(ctx, parent.ExpiresAt); err != nil || n != 1 {
		t.Fatalf("parent purge=%d %v", n, err)
	}
	shutdownTestStore(t, s)
	s = openCandidateContentStore(t, path, &l)
	got, err := s.exchangeContents.Get(ctx, child.ExchangeID, parent.ExpiresAt)
	if err != nil || len(got.Request.Messages) != 3 || got.Request.Messages[1].Blocks[0].Text != parent.Response.Blocks[0].Text {
		t.Fatalf("live descendant lost inherited frames: %v", err)
	}
	p, err := s.exchangeContents.GetPagedProjection(ctx, child.ExchangeID, parent.ExpiresAt, exchangecontent.RequestViewFull)
	if err != nil || p.Request.Messages[len(p.Request.Messages)-1].Blocks[0].Text != "next" {
		t.Fatalf("live child page/tail: %v", err)
	}
	message, err := s.exchangeContents.GetContentPage(ctx, child.ExchangeID, parent.ExpiresAt, p.Request.Messages[1].Blocks[0].Deferred.Cursor)
	if err != nil || message.Total != 1 {
		t.Fatalf("inherited framed page: %v", err)
	}
	if n, err := s.exchangeContents.PurgeExpired(ctx, child.ExpiresAt); err != nil || n != 1 {
		t.Fatalf("last child purge=%d %v", n, err)
	}
	for _, table := range []string{"runtime_exchange_content_blocks", "runtime_exchange_content_block_refs", "runtime_exchange_content_messages", "runtime_exchange_content_transcripts"} {
		if countRows(t, s, table) != 0 {
			t.Fatalf("final expiry retained %s", table)
		}
	}
	sourceDatabaseIntegrity(t, s)
	seedCaptureGraph(t, s, "managed_run", "framed-capture-delete", 1)
	r := sourceOnlyRecord(t, "capture-frame", parent.Response.Blocks[0])
	r.Parent = exchangecontent.ParentRef{CaptureRunID: "framed-capture-delete"}
	if err := s.exchangeContents.Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteCapture(ctx, "managed_run", "framed-capture-delete"); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s, "runtime_exchange_content_blocks") != 0 || countRows(t, s, "runtime_exchange_content_block_refs") != 0 {
		t.Fatal("capture deletion retained frames")
	}
	sourceDatabaseIntegrity(t, s)
	if sourceSchemaHash(t, s) != schema {
		t.Fatal("lifecycle changed sqlite_schema")
	}
}

func TestContentSourcePrefixReplayCheckpointEmptyAndLongerHistory(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	first := sourceOnlyRecord(t, "chain-first", sourceText("first"))
	first.Parent = exchangecontent.ParentRef{CaptureRunID: "source-chain"}
	first.Response = contentRecordFixture(t, "template", first.RecordedAt).Response
	first.Response.Blocks = []exchangecontent.Block{sourceText("answer")}
	if err := s.exchangeContents.Put(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ExchangeID = "chain-second"
	second.Request.Messages = append(append([]exchangecontent.Message{}, first.Request.Messages...), exchangecontent.Message{Role: "assistant", Blocks: first.Response.Blocks}, exchangecontent.Message{Role: "user", Blocks: []exchangecontent.Block{sourceText("next")}})
	second.Response = nil
	if err := s.exchangeContents.Put(ctx, second); err != nil {
		t.Fatal(err)
	}
	p, err := s.exchangeContents.GetProjection(ctx, second.ExchangeID, second.RecordedAt, exchangecontent.RequestViewIncremental)
	if err != nil || len(p.Request.Messages) != 1 || p.Request.Messages[0].Blocks[0].Text != "next" || p.Presentation.InheritedMessageCount != 2 {
		t.Fatalf("suffix: %+v %v", p.Presentation, err)
	}
	replay := second
	replay.ExchangeID = "chain-replay"
	if err := s.exchangeContents.Put(ctx, replay); err != nil {
		t.Fatal(err)
	}
	p, err = s.exchangeContents.GetProjection(ctx, replay.ExchangeID, replay.RecordedAt, exchangecontent.RequestViewIncremental)
	if err != nil || len(p.Request.Messages) != 0 || p.Presentation.Mode != exchangecontent.RequestPresentationSameTranscript {
		t.Fatalf("exact replay: %+v %v", p.Presentation, err)
	}
	checkpoint := sourceOnlyRecord(t, "chain-checkpoint", sourceText("compacted"))
	checkpoint.Parent = first.Parent
	if err := s.exchangeContents.Put(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	p, err = s.exchangeContents.GetProjection(ctx, checkpoint.ExchangeID, checkpoint.RecordedAt, exchangecontent.RequestViewIncremental)
	if err != nil || p.Presentation.Mode != exchangecontent.RequestPresentationCheckpoint {
		t.Fatalf("checkpoint: %v", err)
	}
	empty := checkpoint
	empty.Response = contentRecordFixture(t, "empty-template", first.RecordedAt).Response
	empty.Response.Blocks = nil
	empty.Response.StopReason = string(protocolcore.StopReasonIncomplete)
	if err := s.exchangeContents.Put(ctx, empty); err != nil {
		t.Fatal(err)
	}
	got, err := s.exchangeContents.Get(ctx, empty.ExchangeID, empty.RecordedAt)
	if err != nil || got.Response == nil || len(got.Response.Blocks) != 0 {
		t.Fatalf("empty terminal: %v", err)
	}
	long := sourceOnlyRecord(t, "longer-tiny", sourceText("tiny"))
	long.Request.Messages = make([]exchangecontent.Message, 8193)
	for i := range long.Request.Messages {
		long.Request.Messages[i] = exchangecontent.Message{Role: "user", Blocks: []exchangecontent.Block{sourceText("tiny")}}
	}
	long.Request.Messages[8192].Blocks = []exchangecontent.Block{sourceText("last-tiny")}
	if err := s.exchangeContents.Put(ctx, long); err != nil {
		t.Fatal(err)
	}
	got, err = s.exchangeContents.Get(ctx, long.ExchangeID, long.RecordedAt)
	if err != nil || len(got.Request.Messages) != 8193 || got.Request.Messages[8192].Blocks[0].Text != "last-tiny" {
		t.Fatalf("longer tiny history: %v", err)
	}
}
func sourceText(text string) exchangecontent.Block {
	return exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: text, OriginalSize: len(text)}
}

// This real Store producer is paired with the VM/Chrome strict Dart consumer.
// The checked-in fixture contains only final text/argument pages, but reaching
// them must traverse the entire >PageContentBytes retained text without loss.
func TestContentBlockRetainedIdentityDartFixture(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "identity.db"), &l)
	type example struct {
		Name      string                        `json:"name"`
		CallID    string                        `json:"callId"`
		ToolName  string                        `json:"toolName"`
		Namespace string                        `json:"toolNamespace"`
		Pages     []exchangecontent.ContentPage `json:"pages"`
	}
	var examples []example
	for _, kind := range []string{"tool_call", "tool_result"} {
		for _, namespace := range []bool{false, true} {
			for _, field := range []string{"callId", "toolName", "toolNamespace"} {
				if !namespace && (field == "toolNamespace" || kind == "tool_result" && field == "toolName") {
					continue
				}
				for _, leading := range []bool{true, false} {
					b := exchangecontent.Block{Kind: kind, Availability: exchangecontent.AvailabilityRecorded, CallID: "call", Text: strings.Repeat("x", exchangecontent.PageContentBytes+1)}
					if kind == "tool_call" {
						b.ToolName = "f"
						b.Arguments = json.RawMessage(`{}`)
					}
					if namespace {
						b.ToolName, b.ToolNamespace = "f", "ns"
					}
					v := &b.CallID
					if field == "toolName" {
						v = &b.ToolName
					}
					if field == "toolNamespace" {
						v = &b.ToolNamespace
					}
					if leading {
						*v = "\ufeff" + *v
					} else {
						*v += "\ufeff"
					}
					name := fmt.Sprintf("%s/namespace%t/%s/leading%t", kind, namespace, field, leading)
					r := sourceOnlyRecord(t, fmt.Sprintf("identity-%d", len(examples)), b)
					if err := s.exchangeContents.Put(ctx, r); err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					full, err := s.exchangeContents.Get(ctx, r.ExchangeID, r.RecordedAt)
					if err != nil || !reflect.DeepEqual(full.Request.Messages[0].Blocks[0], b) {
						t.Fatalf("full identity changed %s: %v", name, err)
					}
					p, err := s.exchangeContents.GetPagedProjection(ctx, r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
					if err != nil {
						t.Fatal(err)
					}
					message, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, p.Request.Messages[0].Blocks[0].Deferred.Cursor)
					if err != nil {
						t.Fatal(err)
					}
					cursor := message.Blocks[0].Deferred.Cursor
					e := example{Name: name, CallID: b.CallID, ToolName: b.ToolName, Namespace: b.ToolNamespace}
					var text, arguments strings.Builder
					for cursor != "" {
						page, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, cursor)
						if err != nil {
							t.Fatal(err)
						}
						if page.BlockMetadata == nil || page.BlockMetadata.CallID != b.CallID || page.BlockMetadata.ToolName != b.ToolName || page.BlockMetadata.ToolNamespace != b.ToolNamespace {
							t.Fatal("detail identity changed")
						}
						if page.Kind == "text" {
							text.WriteString(page.Text)
							if page.Offset+len(page.Text) == len(b.Text) {
								e.Pages = append(e.Pages, page)
							}
						} else if page.Kind == "arguments" {
							arguments.WriteString(page.Text)
							e.Pages = append(e.Pages, page)
						} else {
							t.Fatal("ordinary route changed")
						}
						cursor = page.NextCursor
					}
					if text.String() != b.Text || arguments.String() != string(b.Arguments) {
						t.Fatal("ordinary traversal changed")
					}
					examples = append(examples, e)
				}
			}
		}
	}
	encoded, err := json.Marshal(examples)
	if err != nil {
		t.Fatal(err)
	}
	fixture := "// Generated by TestContentBlockRetainedIdentityDartFixture; do not hand-edit.\nconst retainedIdentityPages = r'''\n" + string(encoded) + "\n''';\n"
	if os.Getenv("CONTENT_IDENTITY_FIXTURE_EMIT") == "1" {
		fmt.Println("RETAINED_IDENTITY_FIXTURE=" + string(encoded))
	}
	actual, err := os.ReadFile("../../ui/flutter_app/test/fixtures/retained_identity_pages.dart")
	if err != nil || string(actual) != fixture {
		t.Fatalf("real Store Dart fixture differs (regenerate from producer output): %v", err)
	}
}

func TestContentBlockMetadataPagesAreComplete(t *testing.T) {
	for _, variant := range []string{"source", "kind", "extension"} {
		for _, size := range []int{(128 << 10) + 1, (1 << 20) + 1} {
			for _, body := range []string{"", "short"} {
				if variant == "extension" && body != "" {
					continue
				}
				t.Run(fmt.Sprintf("%s/%d/%s", variant, size, body), func(t *testing.T) {
					l := candidateContentLimits()
					block := exchangecontent.Block{Kind: "reasoning", Availability: exchangecontent.AvailabilityRecorded, Text: body, ProviderSource: strings.Repeat("p", size), ProviderKind: "thinking"}
					if variant == "kind" {
						block.ProviderSource, block.ProviderKind = "provider", strings.Repeat("k", size)
					}
					if variant == "extension" {
						block.Kind = "provider_extension"
						block.Availability = exchangecontent.AvailabilityOmitted
						block.Fingerprint = "sha256:" + strings.Repeat("a", 64)
					}
					r := sourceOnlyRecord(t, "metadata-pages", block)
					source, err := exchangecontent.SourceFromRecordWithin(l, r)
					if err != nil {
						t.Fatal(err)
					}
					cost, err := source.Measure(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					l.RetainedBytes, l.StructureBytes, l.CanonicalBytes = cost.RetainedBytes, cost.StructureBytes, cost.CanonicalBytes
					s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
					if err := putCandidateSource(t, s, source); err != nil {
						t.Fatal(err)
					}
					if _, err := s.exchangeContents.Get(context.Background(), r.ExchangeID, r.RecordedAt); err != nil {
						t.Fatal(err)
					}
					p, err := s.exchangeContents.GetPagedProjection(context.Background(), r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
					if err != nil {
						t.Fatal(err)
					}
					m, err := s.exchangeContents.GetContentPage(context.Background(), r.ExchangeID, r.RecordedAt, p.Request.Messages[0].Blocks[0].Deferred.Cursor)
					if err != nil {
						t.Fatal(err)
					}
					page, err := s.exchangeContents.GetContentPage(context.Background(), r.ExchangeID, r.RecordedAt, m.Blocks[0].Deferred.Cursor)
					if err != nil {
						t.Fatalf("advertised metadata cursor is unusable: %v", err)
					}
					encoded, _ := json.Marshal(page)
					var fields map[string]json.RawMessage
					json.Unmarshal(encoded, &fields)
					if page.Kind != "block_bytes" && len(fields["canonicalCursor"]) == 0 {
						t.Fatal("retained provider metadata has no complete route")
					}
					if page.Kind != "block_bytes" {
						if page.Text != body {
							t.Fatal("ordinary body changed")
						}
						page, err = s.exchangeContents.GetContentPage(context.Background(), r.ExchangeID, r.RecordedAt, page.CanonicalCursor)
						if err != nil {
							t.Fatal(err)
						}
					}
					var complete bytes.Buffer
					for {
						if page.Kind != "block_bytes" || page.Offset != complete.Len() || len(page.Data) == 0 || len(page.Data) > exchangecontent.PageBodyBytes {
							t.Fatal("invalid canonical progress")
						}
						complete.Write(page.Data)
						if page.NextCursor == "" {
							break
						}
						page, err = s.exchangeContents.GetContentPage(context.Background(), r.ExchangeID, r.RecordedAt, page.NextCursor)
						if err != nil {
							t.Fatal(err)
						}
					}
					want, _ := json.Marshal(block)
					if !bytes.Equal(complete.Bytes(), want) {
						t.Fatal("metadata canonical reconstruction changed fields")
					}
				})
			}
		}
	}
}

func TestContentBlockDetailBodiesAndOmittedLocations(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	for _, raw := range []json.RawMessage{[]byte(`[1,2,"\u0061"]`), []byte(`null`), {'"', 0xff, '"'}} {
		block := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "call", ToolName: "f", ToolNamespace: "ns", Text: "first text", Arguments: raw}
		r := sourceOnlyRecord(t, fmt.Sprintf("raw-detail-%x", raw), block)
		if err := s.exchangeContents.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
		p, err := s.exchangeContents.GetPagedProjection(ctx, r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
		if err != nil {
			t.Fatal(err)
		}
		d := p.Request.Messages[0].Blocks[0].Deferred
		if d == nil {
			t.Fatal("non-object or invalidUTF8 Args sent to object-only inline client")
		}
		page, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, d.Cursor)
		if err != nil {
			t.Fatal(err)
		}
		if utf8.Valid(raw) {
			if page.Kind != "text" || page.Text != "first text" || page.BlockMetadata == nil || page.BlockMetadata.ToolNamespace != "ns" {
				t.Fatal("text/metadata missing")
			}
			page, err = s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, page.NextCursor)
			if err != nil || page.Kind != "arguments" || page.Text != string(raw) {
				t.Fatalf("second body lost raw spelling: %v", err)
			}
		} else {
			want, _ := json.Marshal(block)
			if page.Kind != "block_bytes" || !bytes.Equal(page.Data, want) {
				t.Fatal("invalidUTF8 bytes changed")
			}
		}
	}
	block := exchangecontent.Block{Kind: "reasoning", Availability: exchangecontent.AvailabilityOmitted, ProviderSource: "source", ProviderKind: strings.Repeat("k", (1<<20)+1)}
	r := sourceOnlyRecord(t, "omitted-details", block)
	r.Mode = environment.ContentRecordingMetadataOnly
	r.Request.System = []exchangecontent.Block{block}
	r.Response = contentRecordFixture(t, "response", r.RecordedAt).Response
	r.Response.Blocks = []exchangecontent.Block{block}
	source, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := source.Measure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	l.RetainedBytes, l.StructureBytes, l.CanonicalBytes = cost.RetainedBytes, cost.StructureBytes, cost.CanonicalBytes
	s = openCandidateContentStore(t, filepath.Join(t.TempDir(), "omitted.db"), &l)
	if err := s.exchangeContents.Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	ref, err := loadStoredContentReference(ctx, s.reads, r.ExchangeID, r.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{"request", "system", "response"} {
		depth := 0
		if location == "request" {
			depth = 1
		}
		cursor := pageCursor(ref, location, "detail", depth)
		var complete bytes.Buffer
		for encoded := encodeContentCursor(cursor); encoded != ""; {
			page, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, encoded)
			if err != nil {
				t.Fatal(err)
			}
			if page.Kind != "block_bytes" || page.Offset != complete.Len() {
				t.Fatal("omitted details route")
			}
			complete.Write(page.Data)
			encoded = page.NextCursor
		}
		want, _ := json.Marshal(block)
		if !bytes.Equal(complete.Bytes(), want) {
			t.Fatal("omitted fields lost")
		}
		cursor.Kind = "text"
		if _, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, encodeContentCursor(cursor)); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
			t.Fatalf("forged omitted body route: %v", err)
		}
	}
}

func TestContentBlockDetailMetadataBoundaryAndCursorAuthority(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	m := exchangecontent.BlockPageMetadata{Kind: "reasoning", Availability: exchangecontent.AvailabilityRecorded, ProviderSource: "p", ProviderKind: "k", TextBytes: 1}
	encoded, _ := json.Marshal(m)
	for _, delta := range []int{0, 1} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			b := exchangecontent.Block{Kind: m.Kind, Availability: m.Availability, ProviderSource: strings.Repeat("p", exchangecontent.PageContentBytes-len(encoded)+1+delta), ProviderKind: "k", Text: "x"}
			r := sourceOnlyRecord(t, "metadata-boundary", b)
			s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
			if err := s.exchangeContents.Put(ctx, r); err != nil {
				t.Fatal(err)
			}
			ref, err := loadStoredContentReference(ctx, s.reads, r.ExchangeID, r.RecordedAt)
			if err != nil {
				t.Fatal(err)
			}
			cursor := pageCursor(ref, "request", "detail", 1)
			page, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, encodeContentCursor(cursor))
			if err != nil {
				t.Fatal(err)
			}
			if (page.BlockMetadata != nil) != (delta == 0) || (page.CanonicalCursor != "") != (delta == 1) || page.Text != "x" {
				t.Fatal("metadata inline boundary changed")
			}
			for name, mutate := range map[string]func(*contentCursor){
				"root": func(c *contentCursor) { c.Root = strings.Repeat("0", 64) }, "kind": func(c *contentCursor) { c.Kind = "metadata" }, "block": func(c *contentCursor) { c.Block = 1 }, "offset": func(c *contentCursor) { c.Offset = 1 }, "field": func(c *contentCursor) { c.Kind = "arguments" }, "location": func(c *contentCursor) { c.Location = "response" }, "range": func(c *contentCursor) {
					c.Kind = "block_bytes"
					c.Offset = int(exchangecontent.MaxCanonicalBlockBytes) + 1
				},
			} {
				t.Run(name, func(t *testing.T) {
					bad := cursor
					mutate(&bad)
					got, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, encodeContentCursor(bad))
					if err == nil || !reflect.DeepEqual(got, exchangecontent.ContentPage{}) {
						t.Fatal("forged cursor returned provisional data")
					}
				})
			}
		})
	}
}

func TestContentSourceCompletionNormalizesEmptyMetadata(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	base := contentRecordFixture(t, "empty-metadata", time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC))
	request := protocolcore.Request{RequestedModel: "model", EffectiveModel: "model", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "question"}}}}}
	response := protocolcore.Response{ID: "response", RequestedModel: "model", EffectiveModel: "model", ReportedModel: "model", StopReason: protocolcore.StopReasonEndTurn, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "answer"}}}
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingFull, environment.ContentRecordingMetadataOnly} {
		t.Run(string(mode), func(t *testing.T) {
			s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
			policy := environment.ContentRecordingPolicy{Mode: mode, RetentionDays: 1}
			pending, err := exchangecontent.NewSourceWithin(l, base.ExchangeID, base.Frozen, policy, base.RecordedAt, request, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := putCandidateSource(t, s, pending); err != nil {
				t.Fatal(err)
			}
			complete, err := exchangecontent.NewSourceWithin(l, base.ExchangeID, base.Frozen, policy, base.RecordedAt, request, &response)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := loadStoredContentReference(ctx, s.reads, base.ExchangeID, base.RecordedAt)
			if err != nil {
				t.Fatal(err)
			}
			incoming := complete.Metadata()
			t.Logf("stored evidence nil=%v incoming evidence nil=%v; lengths=%d/%d", stored.manifest.Request.ProtocolEvidence == nil, incoming.Request.ProtocolEvidence == nil, len(stored.manifest.Request.ProtocolEvidence), len(incoming.Request.ProtocolEvidence))
			if err := putCandidateSource(t, s, complete); err != nil {
				t.Fatalf("unchanged empty metadata completion: %v", err)
			}
			if _, err := s.exchangeContents.Get(ctx, base.ExchangeID, base.RecordedAt); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestContentSourceExactDenseSourceCanDefer(t *testing.T) {
	l := candidateContentLimits()
	r := sourceOnlyRecord(t, strings.Repeat("a", 128))
	r.Request.Messages[0].Blocks = make([]exchangecontent.Block, 25)
	for i := range r.Request.Messages[0].Blocks {
		r.Request.Messages[0].Blocks[i] = sourceText("")
	}
	source, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := source.Measure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	l.RetainedBytes, l.StructureBytes, l.CanonicalBytes = cost.RetainedBytes, cost.StructureBytes, cost.CanonicalBytes
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	if err := putCandidateSource(t, s, source); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exchangeContents.Get(context.Background(), r.ExchangeID, r.RecordedAt); err != nil {
		t.Fatalf("full exact dense read: %v", err)
	}
	p, err := s.exchangeContents.GetPagedProjection(context.Background(), r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
	if err != nil || len(p.Request.Messages) != 1 || p.Request.Messages[0].Blocks[0].Deferred == nil {
		t.Fatalf("exact dense Source retained=%d cannot represent deferred cursor: %v", cost.RetainedBytes, err)
	}
	page, err := s.exchangeContents.GetContentPage(context.Background(), r.ExchangeID, r.RecordedAt, p.Request.Messages[0].Blocks[0].Deferred.Cursor)
	if err != nil || page.Total != 25 || len(page.Blocks) != 24 || page.NextCursor == "" {
		t.Fatalf("exact dense message continuation: %v", err)
	}
	last, err := s.exchangeContents.GetContentPage(context.Background(), r.ExchangeID, r.RecordedAt, page.NextCursor)
	if err != nil || last.Offset != 24 || len(last.Blocks) != 1 || last.NextCursor != "" {
		t.Fatalf("exact dense final continuation: %v", err)
	}
}

func TestContentSourceExactStructureCanDefer(t *testing.T) {
	l := candidateContentLimits()
	r := sourceOnlyRecord(t, "exact-structure-shell", sourceText(strings.Repeat("x", exchangecontent.PageContentBytes)))
	source, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := source.Measure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	l.RetainedBytes, l.StructureBytes, l.CanonicalBytes = cost.RetainedBytes, cost.StructureBytes, cost.CanonicalBytes
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	if err := putCandidateSource(t, s, source); err != nil {
		t.Fatal(err)
	}
	p, err := s.exchangeContents.GetPagedProjection(context.Background(), r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
	if err != nil || p.Request.Messages[0].Blocks[0].Deferred == nil {
		t.Fatalf("exact structure shell: %v", err)
	}
	message, err := s.exchangeContents.GetContentPage(context.Background(), r.ExchangeID, r.RecordedAt, p.Request.Messages[0].Blocks[0].Deferred.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	for cursor, offset := message.Blocks[0].Deferred.Cursor, 0; cursor != ""; {
		page, err := s.exchangeContents.GetContentPage(context.Background(), r.ExchangeID, r.RecordedAt, cursor)
		if err != nil || page.Offset != offset {
			t.Fatalf("exact structure body continuation: %v", err)
		}
		offset += len(page.Text)
		cursor = page.NextCursor
	}
}

func TestContentSourceRequestPageWithholdsEarlierRows(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	r := sourceOnlyRecord(t, "request-withhold", sourceText("oldest"))
	r.Request.Messages = append(r.Request.Messages, exchangecontent.Message{Role: "assistant", Blocks: []exchangecontent.Block{sourceText("newest")}})
	if err := s.exchangeContents.Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.database.Exec(`UPDATE runtime_exchange_content_blocks SET codec='identity',payload=X'7b' WHERE digest=(SELECT substr(m.block_manifest,1,64) FROM runtime_exchange_content_messages m JOIN runtime_exchange_content_transcripts n ON n.message_digest=m.digest WHERE n.depth=1)`); err != nil {
		t.Fatal(err)
	}
	ref, err := loadStoredContentReference(ctx, s.reads, r.ExchangeID, r.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, encodeContentCursor(pageCursor(ref, "request", "request", 2)))
	if !errors.Is(err, exchangecontent.ErrInvalidEvidence) || !reflect.DeepEqual(page, exchangecontent.ContentPage{}) {
		t.Fatalf("request page disclosed earlier rows after later corruption: messages=%d err=%v", len(page.Messages), err)
	}
}

func TestContentSourceLateErrorSecondPassAndCancellationWithhold(t *testing.T) {
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	r := sourceOnlyRecord(t, "late-withhold", sourceText("earlier"), sourceText(strings.Repeat("&", 6<<20)))
	if err := s.exchangeContents.Put(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	var manifest string
	if err := s.database.QueryRow(`SELECT block_manifest FROM runtime_exchange_content_messages`).Scan(&manifest); err != nil {
		t.Fatal(err)
	}
	last := manifest[len(manifest)-64:]
	ref, err := loadStoredContentReference(context.Background(), s.reads, r.ExchangeID, r.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	real := s.exchangeContents.loadPhysical
	for _, pageKind := range []string{"body", "block_bytes", "detail"} {
		cursor := encodeContentCursor(pageCursor(ref, "request", pageKind, 1))
		for _, kind := range []string{"final-row-error", "second-pass-change", "cancellation"} {
			t.Run(pageKind+"/"+kind, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				lastLoads := 0
				s.exchangeContents.loadPhysical = func(ctx context.Context, d string) (int, string, []byte, error) {
					n, codec, data, err := real(ctx, d)
					if d == last {
						lastLoads++
						if lastLoads == 2 {
							switch kind {
							case "final-row-error":
								return n, codec, data, io.ErrUnexpectedEOF
							case "second-pass-change":
								data = append([]byte(nil), data...)
								data[len(data)-1] ^= 1
							case "cancellation":
								cancel()
							}
						}
					}
					return n, codec, data, err
				}
				t.Cleanup(func() { s.exchangeContents.loadPhysical = real })
				page, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, cursor)
				if err == nil || !reflect.DeepEqual(page, exchangecontent.ContentPage{}) || lastLoads != 2 {
					t.Fatalf("%s returned earlier body: last=%d page=%s err=%v", kind, lastLoads, page.Text, err)
				}
				if kind == "cancellation" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation changed class: %v", err)
				}
				if kind == "final-row-error" && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("final read error changed class: %v", err)
				}
			})
		}
	}
}

func TestContentSourceCanceledTransactionDrains(t *testing.T) {
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	r := sourceOnlyRecord(t, "cancel-write", sourceText("body"))
	source, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := s.database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	before := s.database.Stats().WaitCount
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.exchangeContents.PutSource(ctx, source) }()
	deadline := time.Now().Add(time.Second)
	for s.database.Stats().WaitCount == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.database.Stats().WaitCount == before {
		t.Fatal("Source never reached the held writer")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled Source did not drain")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s, "runtime_exchange_contents") != 0 || countRows(t, s, "runtime_exchange_content_blocks") != 0 {
		t.Fatal("canceled Source published content")
	}
	shutdownTestStore(t, s)
}

func TestContentSourceLegacyUpgradeCodecAndConflictGuards(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	legacy := openTestStore(t, path)
	r := sourceOnlyRecord(t, "legacy-record", sourceText("tiny"), sourceText(strings.Repeat("long legacy", 1024)))
	if err := legacy.exchangeContents.Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	schema := sourceSchemaHash(t, legacy)
	var revision int
	if err := legacy.database.QueryRow(`SELECT schema_revision FROM runtime_metadata`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	l := candidateContentLimits()
	source, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.exchangeContents.PutSource(ctx, source); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("nil policy bypass: %v", err)
	}
	shutdownTestStore(t, legacy)
	s := openCandidateContentStore(t, path, &l)
	got, err := s.exchangeContents.Get(ctx, r.ExchangeID, r.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := json.Marshal(got)
	if err != nil || !bytes.Equal(actual, want) {
		t.Fatalf("legacy canonical bytes changed: %v", err)
	}
	var digest, manifest string
	if err := s.database.QueryRow(`SELECT digest,block_manifest FROM runtime_exchange_content_messages`).Scan(&digest, &manifest); err != nil {
		t.Fatal(err)
	}
	var oldCodecs []string
	for i := 0; i < len(manifest); i += 64 {
		var codec string
		if err := s.database.QueryRow(`SELECT codec FROM runtime_exchange_content_blocks WHERE digest=?`, manifest[i:i+64]).Scan(&codec); err != nil {
			t.Fatal(err)
		}
		oldCodecs = append(oldCodecs, codec)
	}
	if !reflect.DeepEqual(oldCodecs, []string{chunkCodecIdentity, chunkCodecZstd}) {
		t.Fatalf("legacy codec controls=%v", oldCodecs)
	}
	blockDigest := manifest[64:128]
	n, codec, data, err := s.exchangeContents.physicalContentRow(ctx, blockDigest)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decodeStoredPhysicalPayload(blockDigest, n, codec, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.database.Exec(`UPDATE runtime_exchange_content_blocks SET codec='identity',payload=? WHERE digest=?`, plain, blockDigest); err != nil {
		t.Fatal(err)
	}
	replay := r
	replay.ExchangeID = "codec-replay"
	if err := s.exchangeContents.Put(ctx, replay); err != nil {
		t.Fatal(err)
	}
	if err := s.database.QueryRow(`SELECT codec FROM runtime_exchange_content_blocks WHERE digest=?`, blockDigest).Scan(&codec); err != nil || codec != chunkCodecIdentity {
		t.Fatalf("codec-only replay rewrote physical identity: %s %v", codec, err)
	}
	if _, err := s.exchangeContents.Get(ctx, replay.ExchangeID, replay.RecordedAt); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"length", "manifest", "agent"} {
		if kind == "length" {
			_, err = s.database.Exec(`UPDATE runtime_exchange_content_blocks SET plain_bytes=plain_bytes+1 WHERE digest=?`, blockDigest)
		} else if kind == "manifest" {
			_, err = s.database.Exec(`UPDATE runtime_exchange_content_messages SET block_manifest=? WHERE digest=?`, manifest[64:]+manifest[:64], digest)
		} else {
			_, err = s.database.Exec(`UPDATE runtime_exchange_content_messages SET agent_json=? WHERE digest=?`, []byte(`{"agentName":"other"}`), digest)
		}
		if err != nil {
			t.Fatal(err)
		}
		bad := r
		bad.ExchangeID = "conflict-" + kind
		if err := s.exchangeContents.Put(ctx, bad); err == nil {
			t.Fatalf("%s conflict was accepted", kind)
		}
		if _, err := s.exchangeContents.Get(ctx, r.ExchangeID, r.RecordedAt); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
			t.Fatalf("%s conflict read: %v", kind, err)
		}
		if _, err := s.database.Exec(`UPDATE runtime_exchange_content_blocks SET plain_bytes=? WHERE digest=?`, n, blockDigest); err != nil {
			t.Fatal(err)
		}
		if _, err := s.database.Exec(`UPDATE runtime_exchange_content_messages SET block_manifest=?,agent_json=NULL WHERE digest=?`, manifest, digest); err != nil {
			t.Fatal(err)
		}
	}
	broken := append([]byte(nil), plain...)
	broken[len(broken)-1] ^= 1
	if _, err := s.database.Exec(`UPDATE runtime_exchange_content_blocks SET payload=? WHERE digest=?`, broken, blockDigest); err != nil {
		t.Fatal(err)
	}
	damaged := r
	damaged.ExchangeID = "damaged-replay"
	if err := s.exchangeContents.Put(ctx, damaged); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exchangeContents.Get(ctx, damaged.ExchangeID, damaged.RecordedAt); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("damaged shared payload was healed or returned: %v", err)
	}
	if _, err := s.database.Exec(`UPDATE runtime_exchange_content_blocks SET payload=? WHERE digest=?`, plain, blockDigest); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := s.database.QueryRow(`SELECT schema_revision FROM runtime_metadata`).Scan(&after); err != nil || revision != after || sourceSchemaHash(t, s) != schema {
		t.Fatalf("schema/revision changed: %d/%d %v", revision, after, err)
	}
	sourceDatabaseIntegrity(t, s)
}

func TestContentSourceDeferredBoundsAndUTF8ArgumentPages(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	ordinaryBytes := exchangecontent.MaxEncodedBytes - len(`{"kind":"text","availability":"recorded","text":"`) - len(`","originalSize":33554432}`)
	ordinary := sourceOnlyRecord(t, "ordinary-boundary", sourceText(strings.Repeat("x", ordinaryBytes)))
	if err := s.exchangeContents.Put(ctx, ordinary); err != nil {
		t.Fatal(err)
	}
	var physical int
	if err := s.database.QueryRow(`SELECT max(plain_bytes) FROM runtime_exchange_content_blocks`).Scan(&physical); err != nil || physical != exchangecontent.MaxEncodedBytes {
		t.Fatalf("ordinary row boundary=%d %v", physical, err)
	}
	load := s.exchangeContents.loadPhysical
	loads := 0
	s.exchangeContents.loadPhysical = func(ctx context.Context, d string) (int, string, []byte, error) { loads++; return load(ctx, d) }
	p, err := s.exchangeContents.GetPagedProjection(ctx, ordinary.ExchangeID, ordinary.RecordedAt, exchangecontent.RequestViewFull)
	if err != nil || loads != 0 || p.Request.Messages[0].Blocks[0].Deferred == nil {
		t.Fatalf("ordinary32MiB inline loaded=%d %v", loads, err)
	}
	long := sourceOnlyRecord(t, "long-manifest")
	long.Request.Messages[0].Blocks = make([]exchangecontent.Block, 26)
	for i := range long.Request.Messages[0].Blocks {
		long.Request.Messages[0].Blocks[i] = sourceText(fmt.Sprint(i))
	}
	if err := s.exchangeContents.Put(ctx, long); err != nil {
		t.Fatal(err)
	}
	loads = 0
	p, err = s.exchangeContents.GetPagedProjection(ctx, long.ExchangeID, long.RecordedAt, exchangecontent.RequestViewFull)
	if err != nil || loads != 0 || p.Request.Messages[0].Blocks[0].Deferred == nil {
		t.Fatalf("long manifest loaded=%d %v", loads, err)
	}
	message, err := s.exchangeContents.GetContentPage(ctx, long.ExchangeID, long.RecordedAt, p.Request.Messages[0].Blocks[0].Deferred.Cursor)
	if err != nil || message.Total != 26 || len(message.Blocks) != 24 {
		t.Fatalf("logical long-manifest cursor: %v", err)
	}
	last, err := s.exchangeContents.GetContentPage(ctx, long.ExchangeID, long.RecordedAt, message.NextCursor)
	if err != nil || last.Offset != 24 || len(last.Blocks) != 2 || last.Blocks[1].Text != "25" {
		t.Fatalf("logical last window: %v", err)
	}
	args := json.RawMessage(`{"value":"` + strings.Repeat("\u754c", 50000) + `","escaped":"\u0061"}`)
	r := sourceOnlyRecord(t, "argument-pages", exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "call", ToolName: "f", Arguments: args})
	if err := s.exchangeContents.Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	p, err = s.exchangeContents.GetPagedProjection(ctx, r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
	if err != nil {
		t.Fatal(err)
	}
	message, err = s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, p.Request.Messages[0].Blocks[0].Deferred.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	cursor := message.Blocks[0].Deferred.Cursor
	for cursor != "" {
		page, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, cursor)
		if err != nil || page.Kind != "arguments" || !utf8.ValidString(page.Text) || page.Offset != body.Len() {
			t.Fatalf("argument UTF8 page: %v", err)
		}
		body.WriteString(page.Text)
		cursor = page.NextCursor
	}
	if body.String() != string(args) {
		t.Fatal("raw canonical spelling/UTF8 changed across pages")
	}
	ref, err := loadStoredContentReference(ctx, s.reads, r.ExchangeID, r.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	bad := pageCursor(ref, "request", "body", 1)
	bad.Offset = 11 // Inside the first three-byte rune after {"value":".
	if _, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, encodeContentCursor(bad)); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("misaligned UTF8 cursor: %v", err)
	}
	bad = pageCursor(ref, "request", "body", 1)
	bad.Root = strings.Repeat("f", 64)
	if _, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, encodeContentCursor(bad)); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("foreign root cursor: %v", err)
	}
	framedText := strings.Repeat("&", 4<<20) + strings.Repeat("\u754c", 4<<20)
	framed := sourceOnlyRecord(t, "utf8-frame-seam", sourceText(framedText))
	if err := s.exchangeContents.Put(ctx, framed); err != nil {
		t.Fatal(err)
	}
	framedRef, err := loadStoredContentReference(ctx, s.reads, framed.ExchangeID, framed.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	seam := (4 << 20) + 3*((storedFrameBody-len(`{"kind":"text","availability":"recorded","text":"`)-6*(4<<20))/3) - 3
	bodyCursor := pageCursor(framedRef, "request", "body", 1)
	bodyCursor.Offset = seam
	fragment, err := s.exchangeContents.GetContentPage(ctx, framed.ExchangeID, framed.RecordedAt, encodeContentCursor(bodyCursor))
	if err != nil {
		t.Fatal(err)
	}
	end := seam + exchangecontent.PageBodyBytes
	for !utf8.RuneStart(framedText[end]) {
		end--
	}
	if fragment.Offset != seam || fragment.Text != framedText[seam:end] || fragment.Total != len(framedText) {
		t.Fatal("UTF8 changed across physical frame boundary")
	}
}

func TestContentSourceBoundsManifestBytesBeforeScan(t *testing.T) {
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	r := sourceOnlyRecord(t, "manifest-bytes", sourceText("small"))
	if err := s.exchangeContents.Put(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	var digest, original string
	if err := s.database.QueryRow(`SELECT digest,block_manifest FROM runtime_exchange_content_messages`).Scan(&digest, &original); err != nil {
		t.Fatal(err)
	}
	if _, err := s.database.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.database.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.database.Exec(`UPDATE runtime_exchange_content_messages SET block_manifest=? WHERE digest=?`, original, digest); err != nil {
			t.Error(err)
		}
		s.database.Exec(`PRAGMA foreign_keys=ON`)
		s.database.Exec(`PRAGMA ignore_check_constraints=OFF`)
	})
	// SQLite length(TEXT) counts characters. The damaged manifest has an
	// apparently permitted character count but three times the byte bound.
	if _, err := s.database.Exec(`UPDATE runtime_exchange_content_messages SET block_manifest=? WHERE digest=?`, strings.Repeat("\u754c", 1<<19), digest); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := s.exchangeContents.Get(context.Background(), r.ExchangeID, r.RecordedAt)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, exchangecontent.ErrInvalidEvidence) || got.ExchangeID != "" {
		t.Fatalf("damaged manifest: %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 512<<10 {
		t.Fatalf("oversized UTF8 manifest was scanned before byte admission: allocated=%d", allocated)
	}
}

func TestContentSourceReasoningProjectionMatchesLegacy(t *testing.T) {
	l := candidateContentLimits()
	candidate := openCandidateContentStore(t, filepath.Join(t.TempDir(), "candidate.db"), &l)
	legacy := openTestStore(t, filepath.Join(t.TempDir(), "legacy.db"))
	defer shutdownTestStore(t, legacy)
	r := contentRecordFixture(t, "reasoning-parity", time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC))
	b := exchangecontent.Block{Kind: "reasoning", Availability: exchangecontent.AvailabilityRecorded, Text: "thought", OriginalSize: 7, ProviderSource: "anthropic", ProviderKind: "thinking"}
	signature := exchangecontent.Block{Kind: "provider_extension", Availability: exchangecontent.AvailabilityOmitted, OriginalSize: 9, ProviderSource: "anthropic", ProviderKind: "signature", Fingerprint: "sha256:" + strings.Repeat("a", 64)}
	summary := b
	summary.ProviderKind = string(protocolcore.ProviderExtensionReasoningSummary)
	r.Response.Blocks = []exchangecontent.Block{b, summary, signature, sourceText("answer")}
	for _, s := range []*Store{candidate, legacy} {
		if err := s.exchangeContents.Put(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	want, err := legacy.exchangeContents.GetProjection(context.Background(), r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
	if err != nil {
		t.Fatal(err)
	}
	got, err := candidate.exchangeContents.GetProjection(context.Background(), r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reasoning/signature presentation differs: candidate blocks=%d legacy=%d err=%v", len(got.Response.Blocks), len(want.Response.Blocks), err)
	}
}

func TestContentSourceDeferredReadsNoPayloadAndPreviews(t *testing.T) {
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	r := sourceOnlyRecord(t, "deferred-payload", sourceText(strings.Repeat("&", 6<<20)))
	if err := s.exchangeContents.Put(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	loads := 0
	load := s.exchangeContents.loadPhysical
	s.exchangeContents.loadPhysical = func(ctx context.Context, d string) (int, string, []byte, error) { loads++; return load(ctx, d) }
	p, err := s.exchangeContents.GetPagedProjection(context.Background(), r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
	if err != nil || len(p.Request.Messages) != 1 || p.Request.Messages[0].Blocks[0].Deferred == nil || loads != 0 {
		t.Fatalf("deferred directory loads=%d err=%v", loads, err)
	}
	preview, err := s.exchangeContents.RequestPreviews(context.Background(), []string{r.ExchangeID}, r.RecordedAt)
	if err != nil || len(preview) != 0 || loads != 0 {
		t.Fatalf("deferred preview loads=%d previews=%d err=%v", loads, len(preview), err)
	}
	page, err := s.exchangeContents.GetContentPage(context.Background(), r.ExchangeID, r.RecordedAt, p.Request.Messages[0].Blocks[0].Deferred.Cursor)
	if err != nil || page.Total != 1 || loads != 4 {
		t.Fatalf("opened message loads=%d logical=%d err=%v", loads, page.Total, err)
	}
}

func TestContentSourceExactLogicalBudgetCountsOccurrences(t *testing.T) {
	l := candidateContentLimits()
	b := exchangecontent.Block{Kind: "reasoning", Availability: exchangecontent.AvailabilityRecorded, Text: "body", ProviderSource: strings.Repeat("p", 8193), ProviderKind: "kind", Agent: &exchangecontent.AgentContext{AgentName: "agent", Author: "a", Recipient: "b"}}
	r := sourceOnlyRecord(t, "exact-budget", b, b)
	source, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := source.Measure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	l.RetainedBytes, l.StructureBytes, l.CanonicalBytes = cost.RetainedBytes, cost.StructureBytes, cost.CanonicalBytes
	path := filepath.Join(t.TempDir(), "runtime.db")
	s := openCandidateContentStore(t, path, &l)
	if err := putCandidateSource(t, s, source); err != nil {
		t.Fatal(err)
	}
	got, err := s.exchangeContents.Get(context.Background(), r.ExchangeID, r.RecordedAt)
	if err != nil || len(got.Request.Messages) != 1 || len(got.Request.Messages[0].Blocks) != 2 || got.Request.Messages[0].Blocks[1].ProviderSource != b.ProviderSource {
		t.Fatalf("exact logical budget rejected accepted Source: %v", err)
	}
	shutdownTestStore(t, s)
	l.RetainedBytes--
	s = openCandidateContentStore(t, path, &l)
	loads := 0
	load := s.exchangeContents.loadPhysical
	s.exchangeContents.loadPhysical = func(ctx context.Context, d string) (int, string, []byte, error) { loads++; return load(ctx, d) }
	got, err = s.exchangeContents.Get(context.Background(), r.ExchangeID, r.RecordedAt)
	if !errors.Is(err, exchangecontent.ErrInvalidEvidence) || got.ExchangeID != "" || loads != 3 {
		t.Fatalf("budget−1 should reject occurrence two before pass2 allocation: loads=%d record=%s err=%v", loads, got.ExchangeID, err)
	}
	shutdownTestStore(t, s)
	l.RetainedBytes++
	l.StructureBytes--
	s = openCandidateContentStore(t, path, &l)
	loads = 0
	load = s.exchangeContents.loadPhysical
	s.exchangeContents.loadPhysical = func(ctx context.Context, d string) (int, string, []byte, error) { loads++; return load(ctx, d) }
	got, err = s.exchangeContents.Get(context.Background(), r.ExchangeID, r.RecordedAt)
	if !errors.Is(err, exchangecontent.ErrInvalidEvidence) || got.ExchangeID != "" || loads != 3 {
		t.Fatalf("structure−1 must reject before second output: loads=%d err=%v", loads, err)
	}
}
