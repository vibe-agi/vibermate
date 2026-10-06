package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

func TestStoredFragmentWriterRejectsSecondPassMutation(t *testing.T) {
	for _, field := range []string{"agent", "arguments"} {
		t.Run(field, func(t *testing.T) {
			block := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded,
				CallID: "call", ToolName: "tool", Text: strings.Repeat("x", 33<<20),
				Agent: &exchangecontent.AgentContext{Author: "before", Recipient: "recipient"}, Arguments: json.RawMessage(`{"tail":"before"}`)}
			if err := block.Validate(environment.ContentRecordingFull); err != nil {
				t.Fatalf("invalid mutation fixture: %v", err)
			}
			emits := 0
			err := writeStoredBlockRows(context.Background(), block, 64<<20, func(_ string, _ []byte) error {
				emits++
				if emits == 1 {
					if field == "agent" {
						block.Agent.Author = "after!"
					} else {
						copy(block.Arguments, `{"tail":"after!"}`)
					}
				}
				return nil
			})
			if emits != 2 {
				t.Fatalf("fixture emitted %d rows, want two", emits)
			}
			if !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
				t.Fatalf("same-length second-pass %s mutation accepted: %v", field, err)
			}
		})
	}
}

func storedFragmentTestRows(t *testing.T, block exchangecontent.Block) ([]string, map[string][]byte) {
	t.Helper()
	var manifest []string
	rows := map[string][]byte{}
	if err := writeStoredBlockRows(context.Background(), block, 64<<20, func(d string, p []byte) error {
		manifest = append(manifest, d)
		rows[d] = append([]byte(nil), p...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(manifest) != 2 {
		t.Fatalf("fixture has %d rows, want two", len(manifest))
	}
	return manifest, rows
}

func TestStoredFragmentSlotCountBoundaries(t *testing.T) {
	// Independent literal boundaries: 16384 bodies of 33554376 bytes. No
	// hundreds-of-GiB fixture is needed to exercise the production arithmetic.
	for _, tc := range []struct {
		size  uint64
		slots uint32
	}{
		{0, 0}, {1, 1}, {33554376, 1}, {33554431, 1}, {33554432, 1}, {33554433, 2},
		{67108752, 2}, {67108753, 3}, {549754896383, 16384}, {549754896384, 16384},
		{549754896385, 0}, {9223372036854775807, 0}, {18446744073709551615, 0},
	} {
		t.Run(fmt.Sprint(tc.size), func(t *testing.T) {
			got, err := storedBlockSlotCount(tc.size)
			if tc.slots == 0 {
				if got != 0 || !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
					t.Fatalf("invalid length: slots=%d error=%v", got, err)
				}
				return
			}
			if err != nil || got != tc.slots {
				t.Fatalf("slots=%d want=%d error=%v", got, tc.slots, err)
			}
		})
	}
}

func TestStoredFragmentRejectsLogicalHashAtEOF(t *testing.T) {
	block := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: strings.Repeat("x", 33<<20)}
	original, physical := storedFragmentTestRows(t, block)
	wantBytes := int64(len(block.Text) + len(`{"kind":"text","availability":"recorded","text":"","originalSize":0}`))
	for _, change := range []string{"consistent wrong header digest", "rehash changed body tail"} {
		t.Run(change, func(t *testing.T) {
			var manifest string
			rows := map[string][]byte{}
			for ordinal, digest := range original {
				row := append([]byte(nil), physical[digest]...)
				if change == "consistent wrong header digest" {
					for i := 8; i < 40; i++ {
						row[i] = 0xaa
					}
				}
				if change == "rehash changed body tail" && ordinal == 1 {
					row[len(row)-2] = '1'
				}
				sum := sha256.Sum256(row)
				id := hex.EncodeToString(sum[:])
				manifest += id
				rows[id] = row
			}
			loads := 0
			reader, err := newStoredLogicalBlockReader(context.Background(), manifest, 0, 64<<20, func(_ context.Context, d string) (int, string, []byte, error) {
				loads++
				p := rows[d]
				return len(p), "identity", p, nil
			})
			if err != nil {
				t.Fatalf("fixture rejected before body: %v", err)
			}
			n, err := io.Copy(io.Discard, reader)
			if n != wantBytes || loads != 2 || !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
				t.Fatalf("terminal check: bytes=%d want=%d loads=%d error=%v", n, wantBytes, loads, err)
			}
			if n, err := reader.Read(make([]byte, 1)); n != 0 || !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
				t.Fatalf("terminal corruption became success: %d %v", n, err)
			}
		})
	}
}

func TestStoredFragmentRejectsMixedGroupsAndReadCancellation(t *testing.T) {
	block := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: strings.Repeat("x", 33<<20)}
	manifest, rows := storedFragmentTestRows(t, block)
	// A separate legitimate logical value supplies a valid physical tail, but
	// that tail cannot complete the original group.
	block.OriginalSize = 1
	var otherTail string
	emits := 0
	if err := writeStoredBlockRows(context.Background(), block, 64<<20, func(d string, p []byte) error {
		emits++
		if emits == 2 {
			otherTail = d
			rows[d] = append([]byte(nil), p...)
		}
		return nil
	}); err != nil || emits != 2 {
		t.Fatalf("mixed fixture: %d %v", emits, err)
	}
	loads := 0
	load := func(_ context.Context, d string) (int, string, []byte, error) {
		loads++
		p := rows[d]
		return len(p), "identity", p, nil
	}
	mixed, err := newStoredLogicalBlockReader(context.Background(), manifest[0]+otherTail, 0, 64<<20, load)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := io.Copy(io.Discard, mixed); n != 33554376 || loads != 2 || !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("mixed group: bytes=%d loads=%d err=%v", n, loads, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loads = 0
	reader, err := newStoredLogicalBlockReader(ctx, strings.Join(manifest, ""), 0, 64<<20, load)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := reader.Read(make([]byte, 128)); n != 128 || err != nil {
		t.Fatalf("initial read: %d %v", n, err)
	}
	cancel()
	if n, err := io.Copy(io.Discard, reader); n != 0 || loads != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read retained progress: bytes=%d loads=%d err=%v", n, loads, err)
	}
}

func TestStoredFragmentWriterPreservesLateSinkAndContextErrors(t *testing.T) {
	block := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: strings.Repeat("x", 33<<20)}
	sentinel := errors.New("fragment sink stopped")
	for _, stop := range []string{"sink", "context", "final sink after mutation", "final context after mutation"} {
		t.Run(stop, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			emits := 0
			candidate := block
			candidate.Agent = &exchangecontent.AgentContext{Author: "before", Recipient: "recipient"}
			late := strings.HasPrefix(stop, "final")
			err := writeStoredBlockRows(ctx, candidate, 64<<20, func(string, []byte) error {
				emits++
				if late && emits == 1 {
					candidate.Agent.Author = "after!"
					return nil
				}
				if strings.Contains(stop, "sink") {
					return sentinel
				}
				cancel()
				return nil
			})
			want := sentinel
			if strings.Contains(stop, "context") {
				want = context.Canceled
			}
			wantEmits := 1
			if late {
				wantEmits = 2
			}
			if emits != wantEmits || !errors.Is(err, want) {
				t.Fatalf("late %s error: emits=%d err=%v", stop, emits, err)
			}
		})
	}
}

func TestStoredFragmentTamperAndRepeatedGroups(t *testing.T) {
	ctx := context.Background()
	var original []string
	physical := map[string][]byte{}
	if err := writeStoredBlockRows(ctx, exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: strings.Repeat("&", 6<<20)}, 64<<20, func(d string, b []byte) error {
		original = append(original, d)
		physical[d] = append([]byte(nil), b...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(original) != 2 {
		t.Fatalf("fixture groups=%d", len(original))
	}
	tests := []struct {
		name string
		edit func([]string, map[string][]byte) []string
	}{
		{"missing slot", func(m []string, _ map[string][]byte) []string { return m[:1] }},
		{"missing row", func(m []string, r map[string][]byte) []string { delete(r, m[1]); return m }},
		{"reordered", func(m []string, _ map[string][]byte) []string { return []string{m[1], m[0]} }},
		{"repeated ordinal", func(m []string, _ map[string][]byte) []string { return []string{m[0], m[0]} }},
		{"physical substitution", func(m []string, r map[string][]byte) []string {
			b := append([]byte(nil), r[m[1]]...)
			b[len(b)-1] ^= 1
			r[m[1]] = b
			return m
		}},
	}
	for _, kind := range []string{"magic", "logical digest", "total", "ordinal", "count", "trailing", "truncated", "overflow"} {
		kind := kind
		tests = append(tests, struct {
			name string
			edit func([]string, map[string][]byte) []string
		}{kind, func(m []string, r map[string][]byte) []string {
			at := 0
			if kind == "trailing" || kind == "truncated" {
				at = 1
			}
			b := append([]byte(nil), r[m[at]]...)
			switch kind {
			case "magic":
				b[6] = 2
			case "logical digest":
				b[8] ^= 1
			case "total":
				binary.BigEndian.PutUint64(b[40:48], binary.BigEndian.Uint64(b[40:48])+1)
			case "ordinal":
				binary.BigEndian.PutUint32(b[48:52], 1)
			case "count":
				binary.BigEndian.PutUint32(b[52:56], 3)
			case "trailing":
				b = append(b, 0)
			case "truncated":
				b = b[:len(b)-1]
			case "overflow":
				binary.BigEndian.PutUint64(b[40:48], ^uint64(0))
			}
			s := sha256.Sum256(b)
			d := hex.EncodeToString(s[:])
			r[d] = b
			m[at] = d
			return m
		}})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows := map[string][]byte{}
			for d, b := range physical {
				rows[d] = b
			}
			m := tc.edit(append([]string(nil), original...), rows)
			r, err := newStoredLogicalBlockReader(ctx, strings.Join(m, ""), 0, 64<<20, func(_ context.Context, d string) (int, string, []byte, error) {
				b := rows[d]
				return len(b), "identity", b, nil
			})
			if err == nil {
				_, err = io.Copy(io.Discard, r)
			}
			if !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
				t.Fatalf("tamper accepted: %v", err)
			}
		})
	}
	// Repeating a complete group is a second logical occurrence, not corruption.
	manifest := strings.Join(append(append([]string(nil), original...), original...), "")
	for _, slot := range []int{0, 2} {
		r, err := newStoredLogicalBlockReader(ctx, manifest, slot, 64<<20, func(_ context.Context, d string) (int, string, []byte, error) {
			b := physical[d]
			return len(b), "identity", b, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			t.Fatal(err)
		}
		if r.Slots != 2 {
			t.Fatalf("logical occurrence has %d slots", r.Slots)
		}
	}
}

func TestStoredFragmentLimitsCancellationAndSinkErrors(t *testing.T) {
	ctx := context.Background()
	block := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "valid"}
	emits := 0
	emit := func(string, []byte) error { emits++; return nil }
	if err := writeStoredBlockRows(ctx, block, 1, emit); !errors.Is(err, exchangecontent.ErrInvalidEvidence) || emits != 0 {
		t.Fatalf("limit rejected after emission: %d %v", emits, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := writeStoredBlockRows(canceled, block, 1024, emit); !errors.Is(err, context.Canceled) || emits != 0 {
		t.Fatalf("cancellation: %d %v", emits, err)
	}
	sentinel := errors.New("sink stopped")
	if err := writeStoredBlockRows(ctx, block, 1024, func(string, []byte) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("sink error lost: %v", err)
	}
	loads := 0
	load := func(context.Context, string) (int, string, []byte, error) { loads++; return 0, "", nil, nil }
	for _, manifest := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 64*16385)} {
		if _, err := newStoredLogicalBlockReader(ctx, manifest, 0, 1024, load); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
			t.Fatalf("bad manifest accepted: %v", err)
		}
	}
	if loads != 0 {
		t.Fatalf("invalid manifest loaded %d rows", loads)
	}
}

func TestStoredFragmentBoundariesAndLogicalStream(t *testing.T) {
	for _, size := range []int{(32 << 20) - 1, 32 << 20, (32 << 20) + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			block := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "x"}
			probe, err := json.Marshal(block)
			if err != nil {
				t.Fatal(err)
			}
			block.Text = strings.Repeat("x", size-len(probe)+1)
			canonical, err := json.Marshal(block)
			if err != nil || len(canonical) != size {
				t.Fatalf("fixture size %d: %v", len(canonical), err)
			}
			logical := sha256.Sum256(canonical)
			var manifest string
			rows := map[string][]byte{}
			err = writeStoredBlockRows(context.Background(), block, uint64(size), func(digest string, row []byte) error {
				if len(row) > 32<<20 {
					t.Fatalf("physical row too large: %d", len(row))
				}
				sum := sha256.Sum256(row)
				if hex.EncodeToString(sum[:]) != digest {
					t.Fatal("physical identity changed")
				}
				manifest += digest
				rows[digest] = append([]byte(nil), row...)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			wantSlots := 1
			if size > 32<<20 {
				wantSlots = 2
			}
			if len(manifest) != wantSlots*64 {
				t.Fatalf("slots=%d want=%d", len(manifest)/64, wantSlots)
			}
			if wantSlots == 1 {
				if string(rows[manifest]) != string(canonical) {
					t.Fatal("ordinary bytes changed")
				}
			} else {
				for ordinal := 0; ordinal < 2; ordinal++ {
					row := rows[manifest[ordinal*64:(ordinal+1)*64]]
					if string(row[:8]) != "VMECB\x00\x01\x00" || string(row[8:40]) != string(logical[:]) || binary.BigEndian.Uint64(row[40:48]) != uint64(size) || binary.BigEndian.Uint32(row[48:52]) != uint32(ordinal) || binary.BigEndian.Uint32(row[52:56]) != 2 {
						t.Fatalf("frame %d header mismatch", ordinal)
					}
					want := 32 << 20
					if ordinal == 1 {
						want = 113
					}
					if len(row) != want {
						t.Fatalf("frame %d bytes=%d want=%d", ordinal, len(row), want)
					}
				}
			}
			reader, err := newStoredLogicalBlockReader(context.Background(), manifest, 0, uint64(size), func(_ context.Context, digest string) (int, string, []byte, error) {
				row := rows[digest]
				return len(row), "identity", row, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			h := sha256.New()
			n, err := io.Copy(h, reader)
			if err != nil || n != int64(size) || hex.EncodeToString(h.Sum(nil)) != hex.EncodeToString(logical[:]) {
				t.Fatalf("logical stream: bytes=%d err=%v", n, err)
			}
		})
	}
}
