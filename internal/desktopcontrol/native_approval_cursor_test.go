package desktopcontrol

import (
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNativeApprovalCursorBindsStrictPendingTraversal(t *testing.T) {
	capture := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: "capture"}
	at := time.Date(2026, 10, 17, 1, 2, 3, 456000000, time.UTC)
	id := nativetool.ID("11111111111111111111111111111111")
	cursor, err := nativeApprovalCursor(capture, at, id)
	if err != nil {
		t.Fatal(err)
	}
	query, err := parseNativeApprovalQuery(url.Values{"cursor": {cursor}, "limit": {"1"}}.Encode(), capture)
	if err != nil || query.Capture != capture || !query.AfterCreatedAt.Equal(at) || query.AfterID != id || query.Limit != 1 {
		t.Fatalf("tuple roundtrip: %+v %v", query, err)
	}
	for _, raw := range []string{"afterSequence=0", "afterSequence=1", "cursor=", "cursor=" + cursor + "=", "cursor=" + cursor + "&cursor=" + cursor, "cursor=" + strings.Repeat("a", 500), "limit=0", "limit=200", "limit=1&limit=2", "unknown=1", "cursor=%zz"} {
		if _, err := parseNativeApprovalQuery(raw, capture); err == nil {
			t.Fatalf("accepted malformed/legacy query %q", raw)
		}
	}
	foreign := capture
	foreign.ID = "foreign"
	if _, err := parseNativeApprovalQuery("cursor="+cursor, foreign); err == nil {
		t.Fatal("foreign Capture cursor accepted")
	}
	first, err := parseNativeApprovalQuery("", capture)
	if err != nil || !first.AfterCreatedAt.IsZero() || first.AfterID != "" || first.Limit != 50 {
		t.Fatalf("fresh traversal: %+v %v", first, err)
	}
}

// The list across Captures and each Capture's own list traverse different
// sets; a cursor from one must never resume the other.
func TestNativeApprovalCursorsStayInTheirTraversal(t *testing.T) {
	capture := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: "capture"}
	at := time.Date(2026, 11, 19, 1, 2, 3, 456000000, time.UTC)
	now := at.Add(time.Minute)
	id := nativetool.ID("22222222222222222222222222222222")
	all, err := encodeNativeApprovalCursor(nativeAllCapturesCursorDomain, at, id)
	if err != nil {
		t.Fatal(err)
	}
	query, err := parseAllNativeApprovalQuery(url.Values{"cursor": {all}, "limit": {"3"}}.Encode(), now)
	if err != nil || !query.AfterCreatedAt.Equal(at) || query.AfterID != id || query.Limit != 3 || !query.ObservedAt.Equal(now) {
		t.Fatalf("all-Captures roundtrip: %+v %v", query, err)
	}
	if _, err := parseNativeApprovalQuery("cursor="+all, capture); err == nil {
		t.Fatal("all-Captures cursor resumed one Capture's list")
	}
	one, err := nativeApprovalCursor(capture, at, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAllNativeApprovalQuery("cursor="+one, now); err == nil {
		t.Fatal("one Capture's cursor resumed the all-Captures list")
	}
	fresh, err := parseAllNativeApprovalQuery("", now)
	if err != nil || !fresh.AfterCreatedAt.IsZero() || fresh.AfterID != "" || fresh.Limit != 50 {
		t.Fatalf("fresh traversal: %+v %v", fresh, err)
	}
}
