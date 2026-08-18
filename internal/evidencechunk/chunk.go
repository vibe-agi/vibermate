// Package evidencechunk splits observed bytes at content-defined boundaries so
// repeated content is stored once.
//
// The boundaries must be content-defined rather than positional. An Agent
// request carries its whole conversation on every turn, but the part that
// changes each time — per-request transport telemetry inside the top-level
// system parameter — sits at a byte offset the client chooses, not one this
// product can predict. A fixed-size or longest-common-prefix split fails
// completely when the variance lands early; content-defined boundaries
// resynchronize after any local difference regardless of where it occurs. This
// is the property `restic`, `borg` and `casync` rely on.
package evidencechunk

const (
	// MinimumBytes, AverageBytes and MaximumBytes are frozen. Changing them
	// changes where boundaries fall, which orphans every chunk already stored
	// and silently costs a full re-store of every retained capture.
	MinimumBytes = 2 << 10
	AverageBytes = 8 << 10
	MaximumBytes = 64 << 10

	// Normalized chunking uses a stricter mask below the average size and a
	// looser one above it, which concentrates chunk sizes near the average
	// instead of leaving them exponentially distributed. The values are the
	// FastCDC masks for an 8 KiB average at normalization level 2: 15 set bits
	// below the average, 11 above.
	maskBelowAverage uint64 = 0x0003590703530000
	maskAboveAverage uint64 = 0x0000d90003530000
)

// gear maps each byte to a full-width pseudo-random value. It is derived from a
// fixed seed rather than embedded as a literal table so the derivation is
// auditable, and it must never change for the same reason the size bounds must
// not.
var gear = buildGear()

func buildGear() [256]uint64 {
	const golden = 0x9E3779B97F4A7C15
	var table [256]uint64
	state := uint64(golden)
	for index := range table {
		// splitmix64, chosen because it is short enough to verify by reading.
		state += golden
		mixed := state
		mixed = (mixed ^ (mixed >> 30)) * 0xBF58476D1CE4E5B9
		mixed = (mixed ^ (mixed >> 27)) * 0x94D049BB133111EB
		table[index] = mixed ^ (mixed >> 31)
	}
	return table
}

// Split returns the chunks of data in order. Concatenating them reproduces data
// exactly; the returned slices alias data and must not be mutated.
func Split(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	chunks := make([][]byte, 0, len(data)/AverageBytes+1)
	for offset := 0; offset < len(data); {
		size := boundary(data[offset:])
		chunks = append(chunks, data[offset:offset+size])
		offset += size
	}
	return chunks
}

// boundary returns the length of the first chunk of data.
func boundary(data []byte) int {
	if len(data) <= MinimumBytes {
		return len(data)
	}
	limit := min(len(data), MaximumBytes)
	var hash uint64
	for index := MinimumBytes; index < limit; index++ {
		hash = (hash << 1) + gear[data[index]]
		mask := maskAboveAverage
		if index < AverageBytes {
			mask = maskBelowAverage
		}
		if hash&mask == 0 {
			return index + 1
		}
	}
	return limit
}
