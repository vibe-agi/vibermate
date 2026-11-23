package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/ssewire"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

const (
	// maxStreamBytes bounds a recorded stream; a reasoning stream at max
	// effort with summaries is a few megabytes.
	maxStreamBytes = 64 << 20
	// maxErrorBodyBytes bounds a recorded non-stream body.
	maxErrorBodyBytes = 1 << 20
)

// outbound is one request a probe sends.
type outbound struct {
	method   string
	relative string
	header   http.Header
	body     []byte
	timeout  time.Duration
	// cancelOnAccept stops reading once a 200 stream sends its first event,
	// for bodies the upstream should refuse (an accepted one is billed).
	cancelOnAccept bool
}

// timedEvent is one SSE event and when it arrived.
type timedEvent struct {
	name string
	at   time.Duration
	data []byte
}

// exchangeRecord is what came back, measured from just before the send.
type exchangeRecord struct {
	status      int
	header      http.Header
	headerAt    time.Duration
	contentType string
	body        []byte
	stream      []byte
	events      []timedEvent
	// longestByteGap is the longest stretch without any byte, SSE comments
	// included, from the response head on.
	longestByteGap time.Duration
	total          time.Duration
	cancelled      bool
	// transportError is set when no complete answer arrived.
	transportError string
}

type sender interface {
	send(context.Context, outbound) *exchangeRecord
}

// liveSender talks to the provider. It never retries: one probe is one
// request, so the budget is exact.
type liveSender struct {
	base   *url.URL
	key    apiKey
	client *http.Client
	scrub  *scrubber
	agent  string
}

func newLiveSender(base *url.URL, key apiKey, scrub *scrubber, verboseLog io.Writer) *liveSender {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	var roundTripper http.RoundTripper = transport
	if verboseLog != nil {
		roundTripper = &loggingTransport{next: transport, log: verboseLog, scrub: scrub}
	}
	return &liveSender{
		base:   base,
		key:    key,
		client: &http.Client{Transport: roundTripper},
		scrub:  scrub,
		agent:  wireprofile.ViberMateUserAgent(),
	}
}

// validBaseURL admits https anywhere and plain http only on a loopback
// host, so the key never crosses a network in clear text.
func validBaseURL(raw string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("--base-url must be an absolute URL such as https://api.openai.com")
	}
	switch base.Scheme {
	case "https":
	case "http":
		host := base.Hostname()
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("--base-url may use plain http only for a loopback test server")
		}
	default:
		return nil, errors.New("--base-url must use https")
	}
	return base, nil
}

func (live *liveSender) send(parent context.Context, request outbound) *exchangeRecord {
	record := &exchangeRecord{}
	ctx, cancel := context.WithTimeout(parent, request.timeout)
	defer cancel()
	target := live.base.JoinPath(request.relative)
	var body io.Reader
	if request.body != nil {
		body = bytes.NewReader(request.body)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, request.method, target.String(), body)
	if err != nil {
		record.transportError = "request: " + live.scrub.upstream(err.Error(), 300)
		return record
	}
	for name, values := range request.header {
		httpRequest.Header[name] = append([]string(nil), values...)
	}
	if request.body != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	if httpRequest.Header.Get("Accept") == "" {
		httpRequest.Header.Set("Accept", "application/json")
	}
	httpRequest.Header.Set("User-Agent", live.agent)
	httpRequest.Header.Set("Authorization", "Bearer "+live.key.value)

	start := time.Now()
	response, err := live.client.Do(httpRequest)
	if err != nil {
		record.total = time.Since(start)
		record.transportError = live.scrub.upstream(describeTransportError(err), 300)
		return record
	}
	defer response.Body.Close()
	record.status = response.StatusCode
	record.header = response.Header.Clone()
	record.headerAt = time.Since(start)
	record.contentType = response.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(record.contentType)
	if response.StatusCode == http.StatusOK && mediaType == "text/event-stream" {
		readStream(ctx, cancel, response.Body, start, request.cancelOnAccept, record)
	} else {
		content, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		record.body = content
		if err != nil {
			record.transportError = "reading the body: " + live.scrub.upstream(describeTransportError(err), 300)
		}
	}
	record.total = time.Since(start)
	return record
}

func describeTransportError(err error) string {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		err = urlError.Err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	return err.Error()
}

// readStream records every SSE event with its arrival time. It uses its
// own decoder with generous limits, so ViberMate's stream decoder failing
// on the same bytes is a finding, not a lost measurement.
func readStream(ctx context.Context, cancel context.CancelFunc, body io.Reader, start time.Time, cancelOnAccept bool, record *exchangeRecord) {
	decoder, err := ssewire.NewDecoder(ssewire.Options{
		MaxLineBytes:    16 << 20,
		MaxEventBytes:   16 << 20,
		MaxPendingBytes: 16 << 20,
		MaxEvents:       1 << 22,
	})
	if err != nil {
		record.transportError = err.Error()
		return
	}
	buffer := make([]byte, 64<<10)
	lastByte := record.headerAt
	for {
		count, readErr := body.Read(buffer)
		now := time.Since(start)
		if count > 0 {
			if gap := now - lastByte; gap > record.longestByteGap {
				record.longestByteGap = gap
			}
			lastByte = now
			if len(record.stream)+count > maxStreamBytes {
				record.transportError = "the stream exceeded the recording limit"
				cancel()
				return
			}
			record.stream = append(record.stream, buffer[:count]...)
			events, err := decoder.Feed(buffer[:count])
			for _, event := range events {
				timed := timedEvent{name: event.Name, at: now}
				if keepEventData(event.Name) {
					timed.data = bytes.Clone(event.Data)
				}
				record.events = append(record.events, timed)
			}
			if err != nil {
				record.transportError = "SSE framing: " + err.Error()
				cancel()
				return
			}
			if cancelOnAccept && len(record.events) != 0 {
				record.cancelled = true
				cancel()
				return
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				if ctx.Err() != nil {
					record.transportError = "the stream " + describeTransportError(ctx.Err())
				} else {
					record.transportError = "reading the stream: " + readErr.Error()
				}
			} else if err := decoder.Finish(); err != nil {
				record.transportError = "SSE framing at the end: " + err.Error()
			}
			return
		}
	}
}

// keepEventData keeps the payload of the events the report reads; deltas
// are only timed.
func keepEventData(name string) bool {
	switch name {
	case "response.created", "response.completed", "response.incomplete", "response.failed", "error":
		return true
	}
	return false
}

// timing summarises an exchange for the report.
type timing struct {
	FirstHeaderMS int64  `json:"first_header_ms"`
	FirstEventMS  *int64 `json:"first_event_ms,omitempty"`
	FirstEvent    string `json:"first_event,omitempty"`
	// LongestGapMS is the longest stretch between consecutive events, the
	// response head counting as the first: the silence ViberMate's idle
	// timer sees, since keepalive and in_progress events count as progress.
	LongestGapMS     int64    `json:"longest_gap_ms"`
	LongestGapAround []string `json:"longest_gap_between,omitempty"`
	// LongestContentGapMS leaves keepalive, queued and in_progress out.
	LongestContentGapMS int64          `json:"longest_content_gap_ms"`
	LongestByteGapMS    int64          `json:"longest_byte_gap_ms"`
	TotalMS             int64          `json:"total_ms"`
	Events              int            `json:"events"`
	EventCounts         map[string]int `json:"event_counts,omitempty"`
}

func milliseconds(duration time.Duration) int64 { return duration.Milliseconds() }

func (record *exchangeRecord) timing() *timing {
	if record.status == 0 {
		return nil
	}
	summary := &timing{
		FirstHeaderMS:    milliseconds(record.headerAt),
		TotalMS:          milliseconds(record.total),
		Events:           len(record.events),
		LongestByteGapMS: milliseconds(record.longestByteGap),
	}
	if len(record.events) == 0 {
		return summary
	}
	first := milliseconds(record.events[0].at)
	summary.FirstEventMS = &first
	summary.FirstEvent = record.events[0].name
	summary.EventCounts = map[string]int{}
	previous, previousName := record.headerAt, "(response head)"
	contentPrevious := record.headerAt
	var longest, longestContent time.Duration
	for _, event := range record.events {
		summary.EventCounts[event.name]++
		if gap := event.at - previous; gap > longest {
			longest = gap
			summary.LongestGapAround = []string{previousName, event.name}
		}
		previous, previousName = event.at, event.name
		switch event.name {
		case "keepalive", "response.queued", "response.in_progress":
			continue
		}
		if gap := event.at - contentPrevious; gap > longestContent {
			longestContent = gap
		}
		contentPrevious = event.at
	}
	summary.LongestGapMS = milliseconds(longest)
	summary.LongestContentGapMS = milliseconds(longestContent)
	return summary
}

func (record *exchangeRecord) describe() string {
	if record.transportError != "" {
		return record.transportError
	}
	return fmt.Sprintf("HTTP %d", record.status)
}
