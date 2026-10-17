package runtimepersistence

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
)

type inspectionPosition struct {
	Ordinal  uint32
	Boundary int64
}

const inspectionCursorBytes = 117

func inspectionQueryDigest(domain, query string) ([32]byte, error) {
	if len(domain) == 0 || len(domain) > 64 || len(query) > 16384 {
		return [32]byte{}, ErrInspectionCursor
	}
	return sha256.Sum256([]byte(domain + "\x00" + query)), nil
}
func (c *InspectionCatalog) encodeCursor(domain, query string, p inspectionPosition) (string, error) {
	digest, err := inspectionQueryDigest(domain, query)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.maintenance != nil {
		return "", ErrInspectionChanged
	}
	if int(p.Ordinal) >= len(c.sources) || p.Boundary < 0 {
		return "", ErrInspectionCursor
	}
	encoded := make([]byte, inspectionCursorBytes)
	encoded[0] = 1
	set, err := hex.DecodeString(c.version.SourceSet)
	if err != nil {
		return "", err
	}
	copy(encoded[1:33], set)
	binary.BigEndian.PutUint64(encoded[33:41], c.version.Generation)
	copy(encoded[41:73], digest[:])
	binary.BigEndian.PutUint32(encoded[73:77], p.Ordinal)
	binary.BigEndian.PutUint64(encoded[77:85], uint64(p.Boundary))
	mac := hmac.New(sha256.New, c.key[:])
	mac.Write(encoded[:85])
	copy(encoded[85:], mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}
func (c *InspectionCatalog) decodeCursor(domain, query, raw string) (inspectionPosition, error) {
	if len(raw) != base64.RawURLEncoding.EncodedLen(inspectionCursorBytes) {
		return inspectionPosition{}, ErrInspectionCursor
	}
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(encoded) != inspectionCursorBytes {
		return inspectionPosition{}, ErrInspectionCursor
	}
	mac := hmac.New(sha256.New, c.key[:])
	mac.Write(encoded[:85])
	if !hmac.Equal(encoded[85:], mac.Sum(nil)) || encoded[0] != 1 {
		return inspectionPosition{}, ErrInspectionCursor
	}
	digest, err := inspectionQueryDigest(domain, query)
	if err != nil || !hmac.Equal(encoded[41:73], digest[:]) {
		return inspectionPosition{}, ErrInspectionCursor
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.maintenance != nil || hex.EncodeToString(encoded[1:33]) != c.version.SourceSet || binary.BigEndian.Uint64(encoded[33:41]) != c.version.Generation {
		return inspectionPosition{}, ErrInspectionChanged
	}
	p := inspectionPosition{Ordinal: binary.BigEndian.Uint32(encoded[73:77]), Boundary: int64(binary.BigEndian.Uint64(encoded[77:85]))}
	if int(p.Ordinal) >= len(c.sources) || p.Boundary < 0 {
		return inspectionPosition{}, ErrInspectionCursor
	}
	return p, nil
}
