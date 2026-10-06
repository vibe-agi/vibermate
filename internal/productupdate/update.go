// Package productupdate owns the one closed, bounded Runtime release check.
package productupdate

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrUnavailable = errors.New("product update is unavailable")

type Release struct {
	Tag         string    `json:"tag"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"publishedAt"`
}
type Result struct {
	CheckedAt time.Time `json:"checkedAt"`
	Release   Release   `json:"release"`
}
type Transport interface {
	FetchProductRelease(context.Context, egressprofile.Resolved) (*http.Response, error)
}
type Reader struct {
	defaults  egressprofile.Resolver
	transport Transport
	clock     func() time.Time
}

func New(defaults egressprofile.Resolver, transport Transport, clock func() time.Time) (*Reader, error) {
	if defaults == nil || transport == nil || clock == nil {
		return nil, ErrUnavailable
	}
	return &Reader{defaults: defaults, transport: transport, clock: clock}, nil
}

const maximumResponseBytes = 256 * 1024
const operationTimeout = 8 * time.Second

var versionPattern = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)$`)

func (r *Reader) Check(ctx context.Context) (Result, error) {
	if r == nil || ctx == nil {
		return Result{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	checked := r.clock().UTC()
	resolved, err := r.defaults.Resolve(ctx, egressprofile.Inherit(), egressprofile.Inherit())
	if err != nil || ctx.Err() != nil {
		return Result{}, ErrUnavailable
	}
	response, err := r.transport.FetchProductRelease(ctx, resolved)
	if err != nil || response == nil || response.Body == nil {
		return Result{}, ErrUnavailable
	}
	defer response.Body.Close()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(closed); response.Body.Close() })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	if response.StatusCode != http.StatusOK || response.ContentLength > maximumResponseBytes {
		return Result{}, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil || len(body) > maximumResponseBytes || ctx.Err() != nil {
		return Result{}, ErrUnavailable
	}
	release, err := decodeRelease(body)
	if err != nil || ctx.Err() != nil {
		return Result{}, ErrUnavailable
	}
	return Result{CheckedAt: checked, Release: release}, nil
}

func decodeRelease(body []byte) (Release, error) {
	if !utf8.Valid(body) {
		return Release{}, ErrUnavailable
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return Release{}, ErrUnavailable
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return Release{}, ErrUnavailable
		}
		if _, duplicate := fields[key]; duplicate {
			return Release{}, ErrUnavailable
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return Release{}, ErrUnavailable
		}
		fields[key] = value
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return Release{}, ErrUnavailable
	}
	for _, field := range []string{"draft", "prerelease"} {
		if string(fields[field]) != "false" {
			return Release{}, ErrUnavailable
		}
	}
	var release Release
	var published string
	if json.Unmarshal(fields["tag_name"], &release.Tag) != nil || json.Unmarshal(fields["html_url"], &release.URL) != nil || json.Unmarshal(fields["published_at"], &published) != nil {
		return Release{}, ErrUnavailable
	}
	parts := versionPattern.FindStringSubmatch(release.Tag)
	if parts == nil {
		return Release{}, ErrUnavailable
	}
	for _, part := range parts[1:] {
		if _, err := strconv.ParseUint(part, 10, 31); err != nil {
			return Release{}, ErrUnavailable
		}
	}
	if release.URL != "https://github.com/vibe-agi/vibermate/releases/tag/"+release.Tag {
		return Release{}, ErrUnavailable
	}
	release.PublishedAt, err = time.Parse(time.RFC3339Nano, published)
	if err != nil {
		return Release{}, ErrUnavailable
	}
	release.PublishedAt = release.PublishedAt.UTC()
	if name, present := fields["name"]; present && string(name) != "null" {
		if json.Unmarshal(name, &release.Name) != nil || release.Name == "" || len([]rune(release.Name)) > 256 || strings.IndexFunc(release.Name, unicode.IsControl) >= 0 {
			return Release{}, ErrUnavailable
		}
	}
	return release, nil
}
