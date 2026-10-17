package productruntime

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/recording"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

func RunDefaultFourHTTPControlFixture(t *testing.T, count int, controls func(*Runtime, exchange.ResourcePolicy) error) {
	p, err := exchange.DefaultResourcePolicy()
	if err != nil {
		t.Fatal(err)
	}
	testRuntimeLongSessionFixture(t, false, "", false, &longSessionAcceptanceOptions{count: count, policy: p, httpFour: true, controls: func(r *Runtime) error { return controls(r, p) }})
}

// The barrier denotes four admitted synchronous borrowed Sources before the
// sink call. It makes no claim about simultaneous SQLite transactions.
type fourHTTPContentSource struct {
	exchangecontent.Recorder
	sink    exchangecontent.SourceRecorder
	entered chan string
	release chan struct{}
	once    sync.Once
}

func (s *fourHTTPContentSource) RecordSource(ctx context.Context, v *exchangecontent.Source) error {
	if v.Metadata().Response == nil {
		s.entered <- v.Metadata().ExchangeID
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.sink.RecordSource(ctx, v)
}
func (s *fourHTTPContentSource) RecordScopedSource(ctx context.Context, scope recording.Scope, v *exchangecontent.Source) error {
	if v.Metadata().Response == nil {
		s.entered <- v.Metadata().ExchangeID
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.sink.(exchangecontent.ScopedSourceRecorder).RecordScopedSource(ctx, scope, v)
}

type unreadDefaultHTTPBody struct {
	reader *bytes.Reader
	reads  atomic.Int32
}

func (b *unreadDefaultHTTPBody) Read(p []byte) (int, error) { b.reads.Add(1); return b.reader.Read(p) }
func (b *unreadDefaultHTTPBody) Close() error               { return nil }

func testDefaultFourHTTPExecutions(t *testing.T, ctx context.Context, f accountReadFixture, pipeline exchangeRuntime, body []byte, s *fourHTTPContentSource, controls func(*Runtime) error, calls, diagnostics func() int32, count int, tail string) {
	old := f.runtime.exchanges
	f.runtime.exchanges = pipeline
	defer func() { f.runtime.exchanges = old }()
	proxy := f.serveProxy(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(f.runtime.LocalRootCertificate().CertificatePEM())
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, ExpectContinueTimeout: time.Hour}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	type result struct {
		index, status int
		err           error
	}
	done := make(chan result, 5)
	for i := range 4 {
		operation, stop := context.WithCancel(ctx)
		defer stop()
		go func(i int) {
			req, _ := http.NewRequestWithContext(operation, "POST", "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			status := 0
			if resp != nil {
				status = resp.StatusCode
				_, readErr := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if err == nil {
					err = readErr
				}
			}
			done <- result{i, status, err}
		}(i)
	}

	ids := []string{}
	select {
	case id := <-s.entered:
		ids = append(ids, id)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for range 4 {
		select {
		case got := <-done:
			if got.err != nil || got.status != 200 {
				t.Fatalf("optional recording changed HTTP result: %+v", got)
			}
		case <-ctx.Done():
			t.Fatal("optional recording blocked HTTP")
		}
	}
	if err := pipeline.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.bodyAdmission.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := controls(f.runtime); err != nil {
		t.Fatal(err)
	}
	s.once.Do(func() { close(s.release) })
	if err := f.runtime.recorder.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	collecting := true
	for collecting {
		select {
		case id := <-s.entered:
			ids = append(ids, id)
		default:
			collecting = false
		}
	}
	complete := 0
	var expected struct {
		Input []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &expected); err != nil || len(expected.Input) != count {
		t.Fatalf("invalid fixture expectation: %v", err)
	}
	for _, id := range ids {
		record, err := f.runtime.ExchangeContents().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(record.Request.Messages) != count || record.Request.Messages[count-1].Blocks[0].Text != tail {
			t.Fatal("four-active HTTP history incomplete")
		}
		for i, message := range record.Request.Messages {
			if message.Role != expected.Input[i].Role || len(message.Blocks) != 1 || message.Blocks[0].Text != expected.Input[i].Content {
				t.Fatalf("exchange %s changed historical occurrence %d", id, i)
			}
		}
		if record.Response != nil {
			complete++
			if record.Response.ID != "resp_long" || len(record.Response.Blocks) != 1 || record.Response.Blocks[0].Text != "complete-reply" || record.Response.Usage != (exchangecontent.Usage{Output: exchangecontent.UsageValue{Known: true, Tokens: 1, Source: "openai-responses"}}) {
				t.Fatal("uncanceled response incomplete")
			}
		}
	}
	if calls() != 4 || diagnostics() != 0 {
		t.Fatalf("four HTTP completion records=%d upstream=%d diagnostics=%d", complete, calls(), diagnostics())
	}
	t.Log("four real HTTP exchanges completed and released body slots while optional recording remained blocked; accepted snapshots inspected after settlement")
}
