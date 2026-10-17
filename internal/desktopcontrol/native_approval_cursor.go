package desktopcontrol

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"math"
	"net/url"
	"strconv"
	"time"
)

const nativeApprovalCursorBytes = 41

func nativeApprovalCursor(c captureidentity.Reference, at time.Time, id nativetool.ID) (string, error) {
	if c.Validate() != nil || at.IsZero() || at.UnixMilli() <= 0 {
		return "", nativetool.ErrInvalid
	}
	if _, err := nativetool.NewID(string(id)); err != nil {
		return "", nativetool.ErrInvalid
	}
	var payload [nativeApprovalCursorBytes]byte
	payload[0] = 1
	binary.BigEndian.PutUint64(payload[1:9], uint64(at.UnixMilli()))
	if _, err := hex.Decode(payload[9:25], []byte(id)); err != nil {
		return "", nativetool.ErrInvalid
	}
	digest := sha256.Sum256([]byte("native-approval-cursor\x00" + c.Key()))
	copy(payload[25:], digest[:16])
	return base64.RawURLEncoding.EncodeToString(payload[:]), nil
}
func parseNativeApprovalQuery(raw string, c captureidentity.Reference) (nativetool.PendingApprovalRequest, error) {
	q := nativetool.PendingApprovalRequest{Capture: c, Limit: 50}
	if c.Validate() != nil || len(raw) > 512 {
		return q, nativetool.ErrInvalid
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return q, nativetool.ErrInvalid
	}
	for key, v := range values {
		if len(v) != 1 || key != "cursor" && key != "limit" {
			return q, nativetool.ErrInvalid
		}
	}
	if values.Has("limit") {
		q.Limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || q.Limit < 1 || q.Limit > 199 {
			return q, nativetool.ErrInvalid
		}
	}
	if !values.Has("cursor") {
		return q, nil
	}
	value := values.Get("cursor")
	if len(value) != base64.RawURLEncoding.EncodedLen(nativeApprovalCursorBytes) {
		return q, nativetool.ErrInvalid
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(payload) != nativeApprovalCursorBytes || payload[0] != 1 || base64.RawURLEncoding.EncodeToString(payload) != value {
		return q, nativetool.ErrInvalid
	}
	digest := sha256.Sum256([]byte("native-approval-cursor\x00" + c.Key()))
	if subtle.ConstantTimeCompare(payload[25:], digest[:16]) != 1 {
		return q, nativetool.ErrInvalid
	}
	millis := binary.BigEndian.Uint64(payload[1:9])
	if millis == 0 || millis > math.MaxInt64 {
		return q, nativetool.ErrInvalid
	}
	q.AfterCreatedAt = time.UnixMilli(int64(millis)).UTC()
	q.AfterID = nativetool.ID(hex.EncodeToString(payload[9:25]))
	return q, nil
}
