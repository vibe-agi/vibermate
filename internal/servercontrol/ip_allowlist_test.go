package servercontrol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/ipallowlist"
)

type testIPAllowlist struct {
	store    *ipallowlist.Store
	applied  []ipallowlist.List
	refusals IPRefusals
	trusted  ipallowlist.List
}

func (allowlist *testIPAllowlist) Current() ipallowlist.Snapshot { return allowlist.store.Current() }

func (allowlist *testIPAllowlist) Replace(expected int64, list ipallowlist.List) (ipallowlist.Snapshot, error) {
	saved, err := allowlist.store.Replace(expected, list)
	if err == nil {
		allowlist.applied = append(allowlist.applied, saved.List)
	}
	return saved, err
}

func (allowlist *testIPAllowlist) Refusals() IPRefusals { return allowlist.refusals }

func (allowlist *testIPAllowlist) TrustedProxies() ipallowlist.List { return allowlist.trusted }

func newTestIPAllowlist(t *testing.T, local bool) (*ServerIPAllowlistHandler, *testIPAllowlist) {
	t.Helper()
	store, err := ipallowlist.Open(filepath.Join(t.TempDir(), "server-admin"), func() time.Time {
		return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	allowlist := &testIPAllowlist{store: store}
	handler, err := NewServerIPAllowlist(ServerIPAllowlistOptions{Allowlist: allowlist, Local: local})
	if err != nil {
		t.Fatal(err)
	}
	return handler, allowlist
}

func allowlistRequest(t *testing.T, handler http.Handler, method, from, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, ServerIPAllowlistPath, strings.NewReader(body))
	request.RemoteAddr = from
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", recorder.Body.String(), err)
	}
	return body
}

func TestIPAllowlistReportsTheListAndTheRequesterAddress(t *testing.T) {
	t.Parallel()

	handler, allowlist := newTestIPAllowlist(t, false)
	allowlist.refusals = IPRefusals{
		Count: 3, LastAddress: netip.MustParseAddr("198.51.100.4"),
		LastAt: time.Date(2026, 9, 29, 11, 59, 0, 0, time.UTC),
	}
	response := allowlistRequest(t, handler, http.MethodGet, "[::ffff:203.0.113.9]:51000", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", response.Code, response.Body)
	}
	var view serverIPAllowlistView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Schema != ServerIPAllowlistSchema || view.Revision != 0 || len(view.Ranges) != 0 ||
		view.UpdatedAt != "" || view.MaxRanges != ipallowlist.MaxRanges ||
		view.ClientAddress != "203.0.113.9" || view.Refused.Count != 3 ||
		view.Refused.LastAddress != "198.51.100.4" || view.Refused.LastAt != "2026-09-29T11:59:00Z" {
		t.Fatalf("GET view = %+v", view)
	}
	if !strings.Contains(response.Body.String(), `"ranges":[]`) ||
		!strings.Contains(response.Body.String(), `"trustedProxies":[]`) {
		t.Fatalf("empty lists must encode as [], got %s", response.Body)
	}

	balancers, err := ipallowlist.Parse([]string{"10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	allowlist.trusted = balancers
	allowlist.refusals.ProxyHeaderProblems = 4
	behind := allowlistRequest(t, handler, http.MethodGet, "203.0.113.9:51000", "")
	var relayed serverIPAllowlistView
	if err := json.Unmarshal(behind.Body.Bytes(), &relayed); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(relayed.TrustedProxies, []string{"10.0.0.0/24"}) || relayed.ProxyHeaderProblems != 4 {
		t.Fatalf("GET behind a load balancer = %+v", relayed)
	}

	local, _ := newTestIPAllowlist(t, true)
	if body := decodeBody(t, allowlistRequest(t, local, http.MethodGet, "", "")); body["clientAddress"] != nil {
		t.Fatalf("App view named a client address: %v", body)
	}
}

func TestIPAllowlistSavesCanonicalNetworks(t *testing.T) {
	t.Parallel()

	handler, allowlist := newTestIPAllowlist(t, false)
	response := allowlistRequest(t, handler, http.MethodPut, "203.0.113.9:51000",
		`{"revision":0,"ranges":[" 203.0.113.0/24","2001:DB8::/32","203.0.113.0/24"]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", response.Code, response.Body)
	}
	var view serverIPAllowlistView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	want := []string{"203.0.113.0/24", "2001:db8::/32"}
	if view.Revision != 1 || !slices.Equal(view.Ranges, want) || view.UpdatedAt != "2026-09-29T12:00:00Z" {
		t.Fatalf("PUT view = %+v", view)
	}
	if len(allowlist.applied) != 1 || !slices.Equal(allowlist.applied[0].Entries(), want) {
		t.Fatalf("applied lists = %v", allowlist.applied)
	}

	stale := allowlistRequest(t, handler, http.MethodPut, "203.0.113.9:51000",
		`{"revision":0,"ranges":[]}`)
	if stale.Code != http.StatusConflict || decodeBody(t, stale)["code"] != "ip_allowlist_conflict" {
		t.Fatalf("stale PUT status=%d body=%s", stale.Code, stale.Body)
	}
}

func TestIPAllowlistNeverLocksOutTheRequester(t *testing.T) {
	t.Parallel()

	handler, allowlist := newTestIPAllowlist(t, false)
	refused := allowlistRequest(t, handler, http.MethodPut, "198.51.100.4:51000",
		`{"revision":0,"ranges":["203.0.113.0/24"]}`)
	body := decodeBody(t, refused)
	if refused.Code != http.StatusUnprocessableEntity ||
		body["code"] != "ip_allowlist_excludes_requester" || body["clientAddress"] != "198.51.100.4" {
		t.Fatalf("self-excluding PUT status=%d body=%s", refused.Code, refused.Body)
	}
	if allowlist.store.Current().Revision != 0 || len(allowlist.applied) != 0 {
		t.Fatal("a refused list was saved")
	}

	// A session on the Server machine itself is always allowed.
	loopback := allowlistRequest(t, handler, http.MethodPut, "127.0.0.1:51000",
		`{"revision":0,"ranges":["203.0.113.0/24"]}`)
	if loopback.Code != http.StatusOK {
		t.Fatalf("loopback PUT status=%d body=%s", loopback.Code, loopback.Body)
	}

	// The co-located App never crosses the Server listener.
	local, _ := newTestIPAllowlist(t, true)
	if response := allowlistRequest(t, local, http.MethodPut, "",
		`{"revision":0,"ranges":["203.0.113.0/24"]}`); response.Code != http.StatusOK {
		t.Fatalf("App PUT status=%d body=%s", response.Code, response.Body)
	}
}

func TestIPAllowlistExplainsInvalidInput(t *testing.T) {
	t.Parallel()

	handler, allowlist := newTestIPAllowlist(t, false)
	entry := allowlistRequest(t, handler, http.MethodPut, "203.0.113.9:51000",
		`{"revision":0,"ranges":["203.0.113.0/24","198.51.100.7/24"]}`)
	body := decodeBody(t, entry)
	if entry.Code != http.StatusUnprocessableEntity || body["code"] != "ip_allowlist_entry_invalid" ||
		body["entry"] != float64(1) || body["reason"] != ipallowlist.ReasonHostBits ||
		body["suggestion"] != "198.51.100.0/24" {
		t.Fatalf("invalid entry status=%d body=%s", entry.Code, entry.Body)
	}

	ranges := make([]string, ipallowlist.MaxRanges+1)
	for index := range ranges {
		ranges[index] = netip.AddrFrom4([4]byte{10, byte(index >> 8), byte(index), 1}).String()
	}
	payload, err := json.Marshal(map[string]any{"revision": 0, "ranges": ranges})
	if err != nil {
		t.Fatal(err)
	}
	long := allowlistRequest(t, handler, http.MethodPut, "203.0.113.9:51000", string(payload))
	if body := decodeBody(t, long); long.Code != http.StatusUnprocessableEntity ||
		body["code"] != "ip_allowlist_too_long" || body["maxRanges"] != float64(ipallowlist.MaxRanges) {
		t.Fatalf("long list status=%d body=%s", long.Code, long.Body)
	}

	for _, malformed := range []string{
		`{"ranges":[]}`,
		`{"revision":0}`,
		`{"revision":-1,"ranges":[]}`,
		`{"revision":0,"ranges":[],"deny":[]}`,
		`{"revision":0,"ranges":[]} {}`,
		`not json`,
	} {
		response := allowlistRequest(t, handler, http.MethodPut, "203.0.113.9:51000", malformed)
		if response.Code != http.StatusUnprocessableEntity || decodeBody(t, response)["code"] != "invalid_ip_allowlist" {
			t.Fatalf("PUT %s status=%d body=%s", malformed, response.Code, response.Body)
		}
	}
	textRequest := httptest.NewRequest(http.MethodPut, ServerIPAllowlistPath,
		strings.NewReader(`{"revision":0,"ranges":[]}`))
	textRequest.RemoteAddr = "203.0.113.9:51000"
	textRequest.Header.Set("Content-Type", "text/plain")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, textRequest)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("text/plain PUT status=%d", recorder.Code)
	}
	if response := allowlistRequest(t, handler, http.MethodDelete, "203.0.113.9:51000", ""); response.Code != http.StatusNotFound {
		t.Fatalf("DELETE status=%d", response.Code)
	}
	if allowlist.store.Current().Revision != 0 {
		t.Fatal("invalid input changed the list")
	}
}
