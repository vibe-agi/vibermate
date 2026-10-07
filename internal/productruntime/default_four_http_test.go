package productruntime

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
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
	cancelFirst := func() {}
	for i := range 4 {
		operation, stop := context.WithCancel(ctx)
		defer stop()
		if i == 0 {
			cancelFirst = stop
		}
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
	ids := make([]string, 0, 4)
	for range 4 {
		select {
		case id := <-s.entered:
			ids = append(ids, id)
		case result := <-done:
			t.Fatalf("HTTP owner failed before four slots: %+v", result)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	waiting, stopWaiting := context.WithCancel(ctx)
	defer stopWaiting()
	wrote := make(chan struct{})
	var wroteOnce sync.Once
	waiting = httptrace.WithClientTrace(waiting, &httptrace.ClientTrace{WroteHeaders: func() { wroteOnce.Do(func() { close(wrote) }) }})
	unread := &unreadDefaultHTTPBody{reader: bytes.NewReader(body)}
	go func() {
		req, _ := http.NewRequestWithContext(waiting, "POST", "https://chatgpt.com/backend-api/codex/responses", unread)
		req.ContentLength = int64(len(body))
		req.Header.Set("Expect", "100-continue")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		done <- result{4, 0, err}
	}()
	select {
	case <-wrote:
	case <-ctx.Done():
		t.Fatal("waiting headers watchdog")
	}
	if unread.reads.Load() != 0 {
		t.Fatal("waiting fifth HTTP body was read with four Source owners")
	}
	if err := controls(f.runtime); err != nil {
		t.Fatal(err)
	}
	// Actual audit append/terminal/read, independent of model admission.
	attempt := runtimeProviderAttempt(t, "four-http-audit")
	if _, err := f.runtime.egressCompletion.Append(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	terminal, err := attempt.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, CompletedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.egressCompletion.Complete(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	page, err := f.runtime.EgressAttempts().List(ctx, egressaudit.PageRequest{Limit: 10, ExchangeID: attempt.Parent().ExchangeID})
	if err != nil || len(page.Items) != 1 || !page.Items[0].Attempt.Terminal() {
		t.Fatalf("four HTTP audit readback: %v", err)
	}
	stopWaiting()
	select {
	case got := <-done:
		if got.index != 4 || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("waiting cancellation: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("waiting cancellation watchdog")
	}
	cancelFirst()
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if !errors.Is(pipeline.Drain(canceled), context.Canceled) || !errors.Is(f.runtime.bodyAdmission.Drain(canceled), context.Canceled) {
		t.Fatal("four borrowed Sources refunded before physical return")
	}
	s.once.Do(func() { close(s.release) })
	for range 4 {
		select {
		case got := <-done:
			if got.index != 0 && got.index != 4 && (got.err != nil || got.status != 200) {
				t.Fatalf("uncanceled HTTP request failed: %+v", got)
			}
		case <-ctx.Done():
			t.Fatal("HTTP completion watchdog")
		}
	}
	if unread.reads.Load() != 0 {
		t.Fatal("canceled waiting fifth body was consumed")
	}
	if err := pipeline.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.bodyAdmission.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	complete := 0
	for _, id := range ids {
		record, err := f.runtime.ExchangeContents().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(record.Request.Messages) != count || record.Request.Messages[count-1].Blocks[0].Text != tail {
			t.Fatal("four-active HTTP history incomplete")
		}
		if record.Response != nil {
			complete++
			if record.Response.Blocks[0].Text != "complete-reply" {
				t.Fatal("uncanceled response incomplete")
			}
		}
	}
	if complete != 3 || calls() != 3 || diagnostics() != 0 {
		t.Fatalf("four HTTP completion records=%d upstream=%d diagnostics=%d", complete, calls(), diagnostics())
	}
	t.Log("four real admitted HTTP exchanges; fifth body unread; real controls/heartbeat/audit; cancel then complete three records and drain")
}
