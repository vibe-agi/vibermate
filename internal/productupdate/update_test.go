package productupdate

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type updateDefaults struct {
	calls int
	err   error
	delay time.Duration
}

func (d *updateDefaults) Resolve(ctx context.Context, a, f egressprofile.Choice) (egressprofile.Resolved, error) {
	d.calls++
	if a != egressprofile.Inherit() || f != egressprofile.Inherit() {
		return egressprofile.Resolved{}, errors.New("wrong closed choices")
	}
	if d.delay > 0 {
		select {
		case <-time.After(d.delay):
		case <-ctx.Done():
			return egressprofile.Resolved{}, ctx.Err()
		}
	}
	if d.err != nil {
		return egressprofile.Resolved{}, d.err
	}
	return egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionBuiltin, 1)
}

type updateResponse struct {
	body     string
	status   int
	calls    int
	deadline time.Time
}

func (t *updateResponse) FetchProductRelease(ctx context.Context, r egressprofile.Resolved) (*http.Response, error) {
	t.calls++
	t.deadline, _ = ctx.Deadline()
	if r.Source() != egressprofile.SelectionBuiltin || r.DefaultRevision() != 1 {
		return nil, errors.New("frozen exit missing")
	}
	return &http.Response{StatusCode: t.status, ContentLength: -1, Body: io.NopCloser(strings.NewReader(t.body))}, nil
}
func validRelease() map[string]any {
	return map[string]any{"tag_name": "v0.1.24", "name": "Private", "html_url": "https://github.com/vibe-agi/vibermate/releases/tag/v0.1.24", "published_at": "2026-10-05T00:00:00Z", "draft": false, "prerelease": false}
}
func TestReaderRejectsUntrustedReleaseMetadataAndBoundsResponse(t *testing.T) {
	for _, mutation := range []string{"draft", "prerelease", "name-empty", "name-control", "name-long", "version-overflow", "version-suffix", "url-userinfo", "url-port", "url-query", "url-fragment", "url-other-path", "invalid-date", "missing-tag", "duplicate-tag", "trailing", "oversize", "redirect"} {
		t.Run(mutation, func(t *testing.T) {
			payload := validRelease()
			status := 200
			switch mutation {
			case "draft":
				payload["draft"] = true
			case "prerelease":
				payload["prerelease"] = true
			case "name-empty":
				payload["name"] = ""
			case "name-control":
				payload["name"] = "bad\n"
			case "name-long":
				payload["name"] = strings.Repeat("x", 257)
			case "version-overflow":
				payload["tag_name"] = "v2147483648.0.1"
			case "version-suffix":
				payload["tag_name"] = "v0.1.24-beta"
			case "url-userinfo":
				payload["html_url"] = "https://user@github.com/vibe-agi/vibermate/releases/tag/v0.1.24"
			case "url-port":
				payload["html_url"] = "https://github.com:443/vibe-agi/vibermate/releases/tag/v0.1.24"
			case "url-query":
				payload["html_url"] = payload["html_url"].(string) + "?x=1"
			case "url-fragment":
				payload["html_url"] = payload["html_url"].(string) + "#x"
			case "url-other-path":
				payload["html_url"] = "https://github.com/other/releases/tag/v0.1.24"
			case "invalid-date":
				payload["published_at"] = "2026-02-30T00:00:00Z"
			case "missing-tag":
				delete(payload, "tag_name")
			case "redirect":
				status = 302
			}
			encoded, _ := json.Marshal(payload)
			body := string(encoded)
			if mutation == "duplicate-tag" {
				body = strings.Replace(body, `"tag_name":"v0.1.24"`, `"tag_name":"v0.1.24","tag_name":"v0.1.24"`, 1)
			}
			if mutation == "trailing" {
				body += " {}"
			}
			if mutation == "oversize" {
				body = strings.Repeat(" ", maximumResponseBytes+1)
			}
			defaults := &updateDefaults{}
			transport := &updateResponse{body: body, status: status}
			reader, _ := New(defaults, transport, time.Now)
			if result, err := reader.Check(t.Context()); !errors.Is(err, ErrUnavailable) || result != (Result{}) {
				t.Fatalf("untrusted metadata accepted %+v err=%v", result, err)
			}
			if defaults.calls != 1 || transport.calls != 1 {
				t.Fatal("closed check repeated default/send")
			}
		})
	}
}
func TestReaderRejectsInvalidUTF8RatherThanFabricatingReleaseName(t *testing.T) {
	payload, _ := json.Marshal(validRelease())
	body := strings.Replace(string(payload), `"Private"`, "\""+string([]byte{0xff})+"\"", 1)
	reader, _ := New(&updateDefaults{}, &updateResponse{body: body, status: 200}, time.Now)
	if result, err := reader.Check(t.Context()); err == nil {
		t.Fatalf("invalid UTF8 fabricated validated name %q", result.Release.Name)
	}
}
func TestReaderFreezesOnceAndAcceptsOptionalNameWithoutInventingContent(t *testing.T) {
	for _, name := range []string{"absent", "null", "named"} {
		t.Run(name, func(t *testing.T) {
			payload := validRelease()
			if name == "absent" {
				delete(payload, "name")
			}
			if name == "null" {
				payload["name"] = nil
			}
			encoded, _ := json.Marshal(payload)
			defaults := &updateDefaults{}
			transport := &updateResponse{body: string(encoded), status: 200}
			reader, _ := New(defaults, transport, func() time.Time { return time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC) })
			result, err := reader.Check(t.Context())
			if err != nil || result.Release.Tag != "v0.1.24" || defaults.calls != 1 || transport.calls != 1 {
				t.Fatal(result, err)
			}
			if name != "named" && result.Release.Name != "" {
				t.Fatal("missing name fabricated")
			}
		})
	}
	defaults := &updateDefaults{err: errors.New("private persistence details must not leak")}
	transport := &updateResponse{}
	reader, _ := New(defaults, transport, time.Now)
	if _, err := reader.Check(t.Context()); err != ErrUnavailable || transport.calls != 0 {
		t.Fatal("default failure became unset/send/raw error")
	}
}
func TestReaderWholeDeadlineStartsBeforeDefaultResolution(t *testing.T) {
	payload, _ := json.Marshal(validRelease())
	defaults := &updateDefaults{delay: 30 * time.Millisecond}
	transport := &updateResponse{body: string(payload), status: 200}
	reader, _ := New(defaults, transport, time.Now)
	start := time.Now()
	if _, err := reader.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if transport.deadline.After(start.Add(operationTimeout + 10*time.Millisecond)) {
		t.Fatal("8s budget renewed after default resolution")
	}
}

type deadlineBody struct {
	ctx       context.Context
	closed    chan struct{}
	once      sync.Once
	fragments atomic.Int64
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-b.closed:
		return 0, io.ErrClosedPipe
	case <-time.After(15 * time.Millisecond):
		p[0] = ' '
		b.fragments.Add(1)
		return 1, nil
	}
}
func (b *deadlineBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

type composedDeadlineTransport struct {
	body     *deadlineBody
	deadline time.Time
	calls    int
}

func (t *composedDeadlineTransport) FetchProductRelease(ctx context.Context, resolved egressprofile.Resolved) (*http.Response, error) {
	t.calls++
	t.deadline, _ = ctx.Deadline()
	select {
	case <-time.After(30 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	t.body = &deadlineBody{ctx: ctx, closed: make(chan struct{})}
	return &http.Response{StatusCode: 200, ContentLength: -1, Body: t.body}, nil
}
func TestReaderSameDeadlineSpansSlowResolutionSendAndContinuousBody(t *testing.T) {
	// The production8s budget remains; an earlier caller deadline must survive
	// each stage without renewal. Separate actualwire case proves real8s expiry.
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Millisecond)
	defer cancel()
	expected, _ := ctx.Deadline()
	defaults := &updateDefaults{delay: 30 * time.Millisecond}
	transport := &composedDeadlineTransport{}
	reader, _ := New(defaults, transport, time.Now)
	started := time.Now()
	if _, err := reader.Check(ctx); err != ErrUnavailable {
		t.Fatal("composed timeout unsanitized", err)
	}
	if time.Since(started) > time.Second || transport.deadline != expected || defaults.calls != 1 || transport.calls != 1 || transport.body == nil || transport.body.fragments.Load() < 3 {
		t.Fatal("deadline restarted or body did not make progress")
	}
	select {
	case <-transport.body.closed:
	default:
		t.Fatal("expired Reader did not close body before return")
	}
}
