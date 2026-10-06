package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io"
	"math"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

type storedPhysicalLoader func(context.Context, string) (int, string, []byte, error)

const (
	storedFrameMagic    = "VMECB\x00\x01\x00"
	storedFrameHeader   = 56
	storedFrameBody     = exchangecontent.MaxEncodedBytes - storedFrameHeader
	storedManifestSlots = 16384
)

type storedCanonicalMeasure struct {
	ctx            context.Context
	hash           hash.Hash
	bytes, maximum uint64
}

func (w *storedCanonicalMeasure) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if uint64(len(p)) > w.maximum-w.bytes {
		return 0, exchangecontent.ErrInvalidEvidence
	}
	w.bytes += uint64(len(p))
	return w.hash.Write(p)
}

// The callback borrows one physical row until it returns. The writer retains
// only one row, independent of the logical size; compression output belongs to
// the callback and must be charged separately by the transaction owner.
func writeStoredBlockRows(ctx context.Context, block exchangecontent.Block, maximum uint64, emit func(string, []byte) error) error {
	if ctx == nil || emit == nil || maximum == 0 || maximum > math.MaxInt64 {
		return exchangecontent.ErrInvalidEvidence
	}
	m := storedCanonicalMeasure{ctx: ctx, hash: sha256.New(), maximum: maximum}
	if err := exchangecontent.WriteCanonicalBlock(&m, block); err != nil {
		return err
	}
	count := uint64(1)
	header := 0
	if m.bytes > exchangecontent.MaxEncodedBytes {
		count = (m.bytes + storedFrameBody - 1) / storedFrameBody
		header = storedFrameHeader
	}
	if count > storedManifestSlots {
		return exchangecontent.ErrInvalidEvidence
	}
	w := storedRowWriter{ctx: ctx, emit: emit, total: m.bytes, count: uint32(count), header: header}
	copy(w.logical[:], m.hash.Sum(nil))
	w.row = make([]byte, min(uint64(exchangecontent.MaxEncodedBytes), m.bytes+uint64(header)))
	w.reset()
	if err := exchangecontent.WriteCanonicalBlock(&w, block); err != nil {
		return err
	}
	if w.used > w.header {
		if err := w.flush(); err != nil {
			return err
		}
	}
	if w.written != m.bytes || w.ordinal != w.count {
		return exchangecontent.ErrInvalidEvidence
	}
	return ctx.Err()
}

type storedRowWriter struct {
	ctx            context.Context
	emit           func(string, []byte) error
	row            []byte
	used, header   int
	total, written uint64
	ordinal, count uint32
	logical        [sha256.Size]byte
}

func (w *storedRowWriter) reset() {
	w.used = w.header
	if w.header == 0 {
		return
	}
	copy(w.row, storedFrameMagic)
	copy(w.row[8:40], w.logical[:])
	binary.BigEndian.PutUint64(w.row[40:48], w.total)
	binary.BigEndian.PutUint32(w.row[48:52], w.ordinal)
	binary.BigEndian.PutUint32(w.row[52:56], w.count)
}
func (w *storedRowWriter) flush() error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	sum := sha256.Sum256(w.row[:w.used])
	if err := w.emit(hex.EncodeToString(sum[:]), w.row[:w.used]); err != nil {
		return err
	}
	w.ordinal++
	w.reset()
	return nil
}
func (w *storedRowWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if uint64(len(p)) > w.total-w.written {
		return 0, exchangecontent.ErrInvalidEvidence
	}
	written := 0
	for len(p) > 0 {
		n := copy(w.row[w.used:], p)
		w.used += n
		w.written += uint64(n)
		written += n
		p = p[n:]
		if w.used == len(w.row) {
			if err := w.flush(); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

// A logical stream is verified only after EOF. The caller must consume it
// completely before publishing any decoded or selective output.
type storedLogicalBlockReader struct {
	ctx          context.Context
	manifest     string
	start, Slots int
	Size         uint64
	Digest       [sha256.Size]byte
	load         storedPhysicalLoader
	body         []byte
	ordinal      int
	framed       bool
	hash         hash.Hash
	read         uint64
	done         bool
	err          error
}

func newStoredLogicalBlockReader(ctx context.Context, manifest string, slot int, maximum uint64, load storedPhysicalLoader) (*storedLogicalBlockReader, error) {
	if ctx == nil || load == nil || maximum == 0 || maximum > math.MaxInt64 || len(manifest) == 0 || len(manifest)%storedDigestHexBytes != 0 || len(manifest)/storedDigestHexBytes > storedManifestSlots || slot < 0 || slot >= len(manifest)/storedDigestHexBytes {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	r := &storedLogicalBlockReader{ctx: ctx, manifest: manifest, start: slot, load: load, hash: sha256.New()}
	plain, err := r.physical(slot)
	if err != nil {
		return nil, err
	}
	if plain[0] == '{' {
		r.Slots = 1
		r.Size = uint64(len(plain))
		r.Digest = sha256.Sum256(plain)
		r.body = plain
	} else {
		if len(plain) < storedFrameHeader || string(plain[:8]) != storedFrameMagic {
			return nil, exchangecontent.ErrInvalidEvidence
		}
		r.framed = true
		r.Size = binary.BigEndian.Uint64(plain[40:48])
		r.Slots = int(binary.BigEndian.Uint32(plain[52:56]))
		copy(r.Digest[:], plain[8:40])
		if r.Size <= exchangecontent.MaxEncodedBytes || r.Size > maximum || r.Slots < 2 || r.Slots > len(manifest)/storedDigestHexBytes-slot || uint64(r.Slots) != (r.Size+storedFrameBody-1)/storedFrameBody {
			return nil, exchangecontent.ErrInvalidEvidence
		}
		if err := r.accept(plain); err != nil {
			return nil, err
		}
	}
	if r.Size > maximum {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	return r, nil
}
func (r *storedLogicalBlockReader) physical(slot int) ([]byte, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	digest := r.manifest[slot*storedDigestHexBytes : (slot+1)*storedDigestHexBytes]
	if !validStoredDigest(digest) {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	n, codec, payload, err := r.load(r.ctx, digest)
	if err != nil {
		return nil, err
	}
	return decodeStoredPhysicalPayload(digest, n, codec, payload)
}
func (r *storedLogicalBlockReader) accept(plain []byte) error {
	want := uint64(storedFrameBody)
	if r.ordinal == r.Slots-1 {
		want = r.Size - uint64(r.ordinal)*storedFrameBody
	}
	if uint64(len(plain)) != want+storedFrameHeader || string(plain[:8]) != storedFrameMagic || !bytes.Equal(plain[8:40], r.Digest[:]) || binary.BigEndian.Uint64(plain[40:48]) != r.Size || binary.BigEndian.Uint32(plain[48:52]) != uint32(r.ordinal) || binary.BigEndian.Uint32(plain[52:56]) != uint32(r.Slots) {
		return exchangecontent.ErrInvalidEvidence
	}
	r.body = plain[storedFrameHeader:]
	return nil
}
func (r *storedLogicalBlockReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if err := r.ctx.Err(); err != nil {
		r.err = err
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.done {
		return 0, io.EOF
	}
	if len(r.body) == 0 {
		// Drop the previous row before the loader allocates/decompresses another.
		r.body = nil
		r.ordinal++
		if r.ordinal == r.Slots {
			if r.read != r.Size || !bytes.Equal(r.hash.Sum(nil), r.Digest[:]) {
				r.err = exchangecontent.ErrInvalidEvidence
				return 0, r.err
			}
			r.done = true
			return 0, io.EOF
		}
		plain, err := r.physical(r.start + r.ordinal)
		if err == nil {
			err = r.accept(plain)
		}
		if err != nil {
			r.err = err
			return 0, err
		}
	}
	n := copy(p, r.body)
	r.body = r.body[n:]
	r.read += uint64(n)
	_, _ = r.hash.Write(p[:n])
	return n, nil
}

// Authentication precedes classification: a valid JSON value or frame header
// does not establish that the row has the identity its manifest names.
func decodeStoredPhysicalPayload(digest string, plainBytes int, codec string, stored []byte) ([]byte, error) {
	if !validStoredDigest(digest) || plainBytes < 1 || plainBytes > exchangecontent.MaxEncodedBytes || len(stored) == 0 || len(stored) > exchangecontent.MaxEncodedBytes {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	var plain []byte
	switch codec {
	case chunkCodecIdentity:
		plain = stored
	case chunkCodecZstd:
		var err error
		plain, err = bodyDecoder.DecodeAll(stored, make([]byte, 0, plainBytes))
		if err != nil {
			return nil, exchangecontent.ErrInvalidEvidence
		}
	default:
		return nil, exchangecontent.ErrInvalidEvidence
	}
	if len(plain) != plainBytes {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	sum := sha256.Sum256(plain)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	return plain, nil
}
