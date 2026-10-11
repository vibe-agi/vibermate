package productruntime

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

// Rejecting a composed chain at default admission must fail these real HTTP
// cases, as must losing stage order, credentials, complete content or response.
func TestRuntimeDefaultTransformChainHTTP(t *testing.T) {
	for _, scenario := range []string{"transform_http_two", "transform_http_three", "transform_http_excess"} {
		t.Run(scenario, func(t *testing.T) { testRuntimeLongSessionScenario(t, false, scenario, false, 1) })
	}
}

type transformHTTPRecorder struct {
	exchangecontent.Recorder
	sink      exchangecontent.SourceRecorder
	completed chan string
	requests  atomic.Int32
}

func (r *transformHTTPRecorder) RecordSource(ctx context.Context, source *exchangecontent.Source) error {
	if source.Metadata().Response == nil {
		r.requests.Add(1)
	}
	if err := r.sink.RecordSource(ctx, source); err != nil {
		return err
	}
	if source.Metadata().Response != nil {
		r.completed <- source.Metadata().ExchangeID
	}
	return nil
}

func testTransformHTTPExecution(t *testing.T, ctx context.Context, f accountReadFixture, pipeline exchangeRuntime, plan environment.RequestPlan, body []byte, recorder *transformHTTPRecorder, sentinel string, excess bool) {
	t.Helper()
	old := f.runtime.exchanges
	f.runtime.exchanges = pipeline
	defer func() { f.runtime.exchanges = old }()
	proxy := f.serveProxy(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(f.runtime.LocalRootCertificate().CertificatePEM())
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, ExpectContinueTimeout: time.Hour}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	if !excess {
		// Two base leases and this larger chain leave less than one complete
		// chain available. A real HTTP waiter must not consume its body.
		held := make([]*exchange.BodyLease, 3)
		for i := range held {
			var err error
			if i < 2 {
				held[i], err = f.runtime.bodyAdmission.Acquire(ctx)
			} else {
				held[i], err = f.runtime.bodyAdmission.AcquirePlan(ctx, plan)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer held[i].Release()
		}
		waiting, cancel := context.WithCancel(ctx)
		defer cancel()
		wrote := make(chan struct{}, 1)
		waiting = httptrace.WithClientTrace(waiting, &httptrace.ClientTrace{WroteHeaders: func() {
			select {
			case wrote <- struct{}{}:
			default:
			}
		}})
		waitingBody := &unreadDefaultHTTPBody{reader: bytes.NewReader(body)}
		queued, err := http.NewRequestWithContext(waiting, "POST", "https://chatgpt.com/backend-api/codex/responses", waitingBody)
		if err != nil {
			t.Fatal(err)
		}
		queued.ContentLength = int64(len(body))
		queued.Header.Set("Content-Type", "application/json")
		queued.Header.Set("Expect", "100-continue")
		done := make(chan error, 1)
		go func() {
			response, err := client.Do(queued)
			if response != nil {
				response.Body.Close()
			}
			done <- err
		}()
		select {
		case <-wrote:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("waiting request completed without admission")
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if waitingBody.reads.Load() != 0 || recorder.requests.Load() != 0 {
			t.Fatal("mixed-plan waiter read a body or recorded before admission")
		}
		// The next request proceeds only after the larger reservation drains.
		for _, lease := range held {
			lease.Release()
		}
	}
	unread := &unreadDefaultHTTPBody{reader: bytes.NewReader(body)}
	request, err := http.NewRequestWithContext(ctx, "POST", "https://chatgpt.com/backend-api/codex/responses", unread)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Expect", "100-continue")
	request.ContentLength = int64(len(body))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	response.Body.Close()
	if excess {
		if err != nil || response.StatusCode != http.StatusRequestEntityTooLarge || unread.reads.Load() != 0 || recorder.requests.Load() != 0 {
			t.Fatalf("excess plan status=%d body reads=%d records=%d error=%v", response.StatusCode, unread.reads.Load(), recorder.requests.Load(), err)
		}
		if err := f.runtime.bodyAdmission.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil || response.StatusCode != 200 || !bytes.Contains(got, []byte("complete-reply")) {
		t.Fatalf("composed HTTP status=%d body=%s error=%v", response.StatusCode, got, err)
	}
	if err := pipeline.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	var id string
	select {
	case id = <-recorder.completed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	record, err := f.runtime.ExchangeContents().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Request.Messages) != 1 || record.Request.Messages[0].Role != "user" || len(record.Request.Messages[0].Blocks) != 1 || record.Request.Messages[0].Blocks[0].Text != sentinel || record.Response == nil || record.Response.ID != "resp_long" || len(record.Response.Blocks) != 1 || record.Response.Blocks[0].Text != "complete-reply" || !record.Response.Usage.Output.Known || record.Response.Usage.Output.Tokens != 1 {
		t.Fatalf("incomplete stored exchange: %+v", record)
	}
	if err := f.runtime.bodyAdmission.Drain(ctx); err != nil {
		t.Fatal(err)
	}
}
