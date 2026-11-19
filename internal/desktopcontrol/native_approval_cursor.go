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

// A cursor is bound to the traversal that issued it: one Capture's, or the
// list across every Capture. Neither is accepted by the other.
func nativeCaptureCursorDomain(c captureidentity.Reference) string {
	return "native-approval-cursor\x00" + c.Key()
}

const nativeAllCapturesCursorDomain = "native-approval-cursor-all-captures\x00"

func nativeApprovalCursor(c captureidentity.Reference, at time.Time, id nativetool.ID) (string, error) {
	if c.Validate() != nil {
		return "", nativetool.ErrInvalid
	}
	return encodeNativeApprovalCursor(nativeCaptureCursorDomain(c), at, id)
}
func encodeNativeApprovalCursor(domain string, at time.Time, id nativetool.ID) (string, error) {
	if at.IsZero() || at.UnixMilli() <= 0 {
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
	digest := sha256.Sum256([]byte(domain))
	copy(payload[25:], digest[:16])
	return base64.RawURLEncoding.EncodeToString(payload[:]), nil
}
func parseNativeApprovalQuery(raw string, c captureidentity.Reference) (nativetool.PendingApprovalRequest, error) {
	q := nativetool.PendingApprovalRequest{Capture: c, Limit: 50}
	if c.Validate() != nil {
		return q, nativetool.ErrInvalid
	}
	page, err := parseNativeApprovalPage(raw, nativeCaptureCursorDomain(c))
	q.AfterCreatedAt, q.AfterID, q.Limit = page.AfterCreatedAt, page.AfterID, page.Limit
	return q, err
}
func parseAllNativeApprovalQuery(raw string, now time.Time) (nativetool.AllPendingApprovalRequest, error) {
	q, err := parseNativeApprovalPage(raw, nativeAllCapturesCursorDomain)
	q.ObservedAt = now
	return q, err
}

// parseNativeApprovalPage reads limit and cursor, nothing else.
func parseNativeApprovalPage(raw, domain string) (nativetool.AllPendingApprovalRequest, error) {
	q := nativetool.AllPendingApprovalRequest{Limit: 50}
	if len(raw) > 512 {
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
	digest := sha256.Sum256([]byte(domain))
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
