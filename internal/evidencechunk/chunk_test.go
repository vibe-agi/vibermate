package evidencechunk

import (
	"bytes"
	"testing"
)

func TestSplitReassemblesEveryInputExactly(t *testing.T) {
	t.Parallel()

	for name, input := range map[string][]byte{
		"empty":          {},
		"single byte":    {0x7A},
		"below minimum":  bytes.Repeat([]byte{0x41}, MinimumBytes-1),
		"at minimum":     bytes.Repeat([]byte{0x42}, MinimumBytes),
		"at average":     bytes.Repeat([]byte{0x43}, AverageBytes),
		"at maximum":     bytes.Repeat([]byte{0x44}, MaximumBytes),
		"above maximum":  bytes.Repeat([]byte{0x45}, MaximumBytes+1),
		"highly regular": bytes.Repeat([]byte("the same line over and over\n"), 4096),
	} {
		chunks := Split(input)
		var rebuilt []byte
		for _, chunk := range chunks {
			rebuilt = append(rebuilt, chunk...)
		}
		if !bytes.Equal(rebuilt, input) {
			t.Fatalf("%s: reassembly lost bytes (%d in, %d out)",
				name, len(input), len(rebuilt))
		}
	}
}

func TestSplitHonorsItsSizeBounds(t *testing.T) {
	t.Parallel()

	input := make([]byte, 512<<10)
	for index := range input {
		// A deterministic non-repeating pattern so boundaries actually fire.
		input[index] = byte(index*31 + index/257)
	}

	chunks := Split(input)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want a split input", len(chunks))
	}
	for index, chunk := range chunks {
		if len(chunk) > MaximumBytes {
			t.Fatalf("chunk %d is %d bytes, above the maximum", index, len(chunk))
		}
		last := index == len(chunks)-1
		if !last && len(chunk) < MinimumBytes {
			t.Fatalf("chunk %d is %d bytes, below the minimum", index, len(chunk))
		}
	}
}

// The point of content-defined boundaries is that a change early in the stream
// does not invalidate everything after it. A fixed-size or prefix-based split
// fails exactly here, which is why this product cannot use one: the volatile
// region of an Agent request lives inside the system parameter, whose byte
// offset is chosen by the client.
func TestSplitResynchronizesAfterAnEarlyEdit(t *testing.T) {
	t.Parallel()

	original := make([]byte, 256<<10)
	for index := range original {
		original[index] = byte(index*17 + index/97)
	}
	edited := append([]byte("x-telemetry: 4f2a1c\n"), original...)

	shared := make(map[string]struct{})
	for _, chunk := range Split(original) {
		shared[string(chunk)] = struct{}{}
	}
	reused := 0
	editedChunks := Split(edited)
	for _, chunk := range editedChunks {
		if _, ok := shared[string(chunk)]; ok {
			reused++
		}
	}
	if reused*4 < len(editedChunks)*3 {
		t.Fatalf(
			"only %d of %d chunks were reused after an early edit; "+
				"content-defined chunking did not resynchronize",
			reused, len(editedChunks),
		)
	}
}
