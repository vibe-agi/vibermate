package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"io"
	"strings"
	"testing"
)

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
