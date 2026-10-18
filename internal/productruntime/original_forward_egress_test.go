package productruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

const originalForwardRawPath = "/backend-api/plugins/featured?platform=synthetic%2Bfixture"
const originalForwardBody = `{"model":"synthetic","input":"hello","stream":true}`
const originalForwardPrefix = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"original-forward\",\"model\":\"synthetic\",\"status\":\"in_progress\",\"output\":[]}}\n\n: retained raw comment\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"original-delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_original\"}\n\n"
const originalForwardTerminal = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"original-forward\",\"model\":\"synthetic\",\"created_at\":1,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"

// These tests catch choosing the current default after admission or a flushed
// stream, bypassing catalog/compiled destination authority, credential changes,
// extra GET/POST/dials, opaque byte changes, transform loss, and orphan audits.
func TestActualRuntimeOriginalRawEgressMatrix(t *testing.T) {
	for _, choice := range []string{"flow_proxy", "flow_direct", "global", "builtin"} {
		t.Run(choice, func(t *testing.T) {
			f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "raw", choice: choice, edge: "send_edit"})
			next := forwardNextExpected(choice)
			run := newOriginalForwardRun(t, f)
			first := run.start(t)
			first.waitEntry(t, f.entered)
			assertCapturedPhysical(t, f, 0, 0, 0, 0)
			f.editDefault(t)
			f.unblock()
			assertOriginalForwardReply(t, f, first.join(t), false)
			a, b, direct := forwardPhysical(choice, false)
			assertCapturedPhysical(t, f, 1, a, b, direct)
			f.assertAudits(t, 1)
			f.mu.Lock()
			f.expected = next
			f.mu.Unlock()
			assertOriginalForwardReply(t, f, run.start(t).join(t), false)
			a2, b2, d2 := forwardPhysical(choice, true)
			f.assertAudits(t, 2)
			f.drain(t)
			assertCapturedPhysical(t, f, 2, a+a2, b+b2, direct+d2)
		})
	}
}

func TestActualRuntimeOriginalTransformedEgressMatrix(t *testing.T) {
	for _, family := range []string{"transformed", "original_model"} {
		choices := []string{"flow_proxy", "flow_direct", "global", "builtin"}
		if family == "original_model" {
			// Existing G positive/frozen/down model evidence is carried.
			choices = []string{"flow_proxy", "flow_direct", "builtin"}
		}
		for _, choice := range choices {
			t.Run(family+"/"+choice, func(t *testing.T) {
				f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: family, choice: choice, edge: "stream_edit"})
				next := forwardNextExpected(choice)
				run := newOriginalForwardRun(t, f)
				first := run.start(t)
				first.waitEntry(t, f.streamEntered)
				assertOriginalForwardReply(t, f, first.firstChunk(t), true)
				a, b, direct := forwardPhysical(choice, false)
				assertCapturedPhysical(t, f, 1, a, b, direct)
				f.editDefault(t)
				f.unblock()
				assertOriginalForwardReply(t, f, first.join(t), false)
				run.joinProducers(t)
				f.assertAudits(t, 1)
				f.mu.Lock()
				f.expected = next
				f.mu.Unlock()
				assertOriginalForwardReply(t, f, run.start(t).join(t), false)
				run.joinProducers(t)
				a2, b2, d2 := forwardPhysical(choice, true)
				f.assertAudits(t, 2)
				f.drain(t)
				assertCapturedPhysical(t, f, 2, a+a2, b+b2, direct+d2)
			})
		}
	}
}

func TestActualRuntimeOriginalForwardHoldAndFailure(t *testing.T) {
	rows := []struct{ family, choice, edge string }{
		{"raw", "global", "hold_edit"}, {"raw", "flow_proxy", "hold_edit"}, {"raw", "flow_direct", "hold_edit"}, {"raw", "builtin", "hold_edit"},
		{"raw", "global", "held_cancel"}, {"raw", "global", "held_failed_probe"},
		{"raw", "flow_proxy", "proxy_loss"}, {"raw", "global", "proxy_loss"},
		{"raw", "flow_direct", "target_dial_failure"}, {"raw", "builtin", "target_dial_failure"}, {"raw", "flow_direct", "strict_tls_failure"}, {"raw", "builtin", "strict_tls_failure"},
		{"original_model", "global", "hold_edit"}, {"original_model", "global", "held_cancel"}, {"original_model", "global", "held_failed_probe"},
		{"original_model", "flow_direct", "target_dial_failure"}, {"original_model", "builtin", "target_dial_failure"}, {"original_model", "flow_direct", "strict_tls_failure"}, {"original_model", "builtin", "strict_tls_failure"}, {"original_model", "global", "stream_disconnect"},
		{"transformed", "global", "hold_edit"}, {"transformed", "flow_proxy", "hold_edit"}, {"transformed", "flow_direct", "hold_edit"}, {"transformed", "builtin", "hold_edit"},
		{"transformed", "global", "held_cancel"}, {"transformed", "global", "held_failed_probe"}, {"transformed", "flow_proxy", "proxy_loss"}, {"transformed", "global", "proxy_loss"},
		{"transformed", "flow_direct", "target_dial_failure"}, {"transformed", "builtin", "target_dial_failure"}, {"transformed", "flow_direct", "strict_tls_failure"}, {"transformed", "builtin", "strict_tls_failure"}, {"transformed", "global", "stream_disconnect"},
	}
	for _, row := range rows {
		t.Run(row.family+"/"+row.choice+"/"+row.edge, func(t *testing.T) {
			f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: row.family, choice: row.choice, edge: row.edge})
			next := forwardNextExpected(row.choice)
			run := newOriginalForwardRun(t, f)
			held := strings.HasPrefix(row.edge, "held_") || row.edge == "hold_edit"
			if held {
				if _, err := f.r.EnterOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
					t.Fatal(err)
				}
			} else if row.edge == "target_dial_failure" {
				f.network.failDirect.Store(true)
			}
			first := run.start(t)
			if row.edge == "stream_disconnect" {
				first.waitEntry(t, f.streamEntered)
				prefix := first.firstChunk(t)
				assertOriginalForwardReply(t, f, prefix, true)
				f.editDefault(t)
				first.cancel()
				reply := first.join(t)
				t.Logf("client-canceled original stream family=%s status=%d bytes=%d readErr=%v", row.family, reply.Status, len(reply.Body), reply.Err)
				// HTTP EOF and cancellation errors both permit an incomplete SSE
				// prefix; neither proves a semantic or audited success.
				assertOriginalForwardBody(t, f, reply, true)
				if !bytes.Equal(reply.Body, prefix.Body) {
					t.Fatalf("client cancellation appended or changed downstream prefix: before=%q after=%q", prefix.Body, reply.Body)
				}
				run.joinProducers(t)
				f.assertAudits(t, 1)
				f.drain(t)
				assertCapturedPhysical(t, f, 1, 1, 0, 0)
				return
			}
			if row.edge == "proxy_loss" {
				first.waitEntry(t, f.entered)
				assertCapturedPhysical(t, f, 0, 0, 0, 0)
				f.editDefault(t)
				if err := f.proxyA.Close(); err != nil {
					t.Fatal(err)
				}
				f.unblock()
			}
			if !held {
				reply := first.join(t)
				if reply.Err != nil || reply.Status == http.StatusOK || reply.Status == 0 {
					t.Fatalf("original transport failure reply: %+v", reply)
				}
				f.assertAudits(t, 1)
				f.drain(t)
				if row.edge == "proxy_loss" {
					assertCapturedPhysical(t, f, 0, 0, 0, 0, 1, 0)
				} else {
					assertCapturedPhysical(t, f, 0, 0, 0, 1)
				}
				return
			}
			targets := f.waitQueue(t)
			assertOriginalForwardTarget(t, f, targets)
			assertCapturedPhysical(t, f, 0, 0, 0, 0)
			f.assertAudits(t, 0)
			f.editDefault(t)
			if row.edge == "held_cancel" {
				first.cancel()
				if reply := first.join(t); !errors.Is(reply.Err, context.Canceled) {
					t.Fatalf("canceled original reply: %+v", reply)
				}
				f.drain(t)
				if _, err := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
					t.Fatal("empty original queue resume", err)
				}
				f.drain(t)
				assertCapturedPhysical(t, f, 0, 0, 0, 0)
				f.assertAudits(t, 0)
				return
			}
			if row.edge == "held_failed_probe" {
				if err := f.proxyA.Close(); err != nil {
					t.Fatal(err)
				}
			}
			_, resumeErr := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision)
			if row.edge == "held_failed_probe" {
				s := f.gate.Snapshot()
				if resumeErr == nil || s.State != offlinehold.StateHeld || s.QueuedRequests != 1 || s.LastProbeReason != offlinehold.ProbeReasonTransportUnavailable {
					t.Fatalf("failed original probe released queue: snapshot=%+v err=%v", s, resumeErr)
				}
				first.cancel()
				first.join(t)
				f.gate.BeginShutdown()
				f.drain(t)
				assertCapturedPhysical(t, f, 0, 0, 0, 0, 1, 0)
				f.assertAudits(t, 0)
				t.Logf("concrete failed probe retained queue; canceled caller joined; reason=%s", s.LastProbeReason)
				return
			}
			if resumeErr != nil {
				t.Fatal("original resume", resumeErr)
			}
			assertOriginalForwardReply(t, f, first.join(t), false)
			run.joinProducers(t)
			a, b, direct := forwardPhysical(row.choice, false)
			assertCapturedPhysical(t, f, 1, 2*a, 2*b, 2*direct)
			f.assertAudits(t, 1)
			f.mu.Lock()
			f.expected = next
			f.mu.Unlock()
			assertOriginalForwardReply(t, f, run.start(t).join(t), false)
			run.joinProducers(t)
			a2, b2, d2 := forwardPhysical(row.choice, true)
			f.assertAudits(t, 2)
			f.drain(t)
			assertCapturedPhysical(t, f, 2, 2*a+a2, 2*b+b2, 2*direct+d2)
		})
	}
}

// Literal input facts, chosen before SQL observations. No expected selection is
// derived from an observed audit or a production resolution helper.
func forwardNextExpected(choice string) capturedOriginalExpected {
	switch choice {
	case "flow_proxy":
		return capturedOriginalExpected{Source: egressprofile.SelectionFlow, ProfileID: "profile.captured-original-a"}
	case "flow_direct":
		return capturedOriginalExpected{Source: egressprofile.SelectionFlow, ProfileID: "profile.direct"}
	case "global":
		return capturedOriginalExpected{Source: egressprofile.SelectionBuiltin, ProfileID: "profile.direct", DefaultRevision: 3}
	case "builtin":
		return capturedOriginalExpected{Source: egressprofile.SelectionGlobal, ProfileID: "profile.captured-original-b", DefaultRevision: 2}
	default:
		panic("unlisted original selection")
	}
}

func forwardPhysical(choice string, next bool) (a, b, direct int64) {
	switch choice {
	case "flow_proxy":
		return 1, 0, 0
	case "flow_direct":
		return 0, 0, 1
	case "global":
		if next {
			return 0, 0, 1
		}
		return 1, 0, 0
	case "builtin":
		if next {
			return 0, 1, 0
		}
		return 0, 0, 1
	default:
		panic("unlisted physical selection")
	}
}

type originalForwardFlight struct {
	done   chan capturedOriginalReply
	chunk  chan capturedOriginalReply
	cancel context.CancelFunc
	joined bool
}

type originalForwardRun struct {
	f         *capturedOriginalFixture
	flights   []*originalForwardFlight
	producers int64
}

func newOriginalForwardRun(t *testing.T, f *capturedOriginalFixture) *originalForwardRun {
	run := &originalForwardRun{f: f}
	t.Cleanup(func() {
		// Cancel/release/join callers and server producers before draining. This
		// context is independent of t.Context(), which Cleanup already canceled.
		for _, flight := range run.flights {
			flight.cancel()
		}
		f.unblock()
		f.gate.BeginShutdown()
		f.r.exchanges.BeginShutdown()
		for _, flight := range run.flights {
			if !flight.joined {
				select {
				case <-flight.done:
					flight.joined = true
				case <-time.After(4 * time.Second):
					t.Error("original cleanup caller join deadline")
				}
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if f.producerDone != nil {
		producerJoin:
			for run.producers < f.httpCalls.Load() {
				select {
				case <-f.producerDone:
					run.producers++
				case <-ctx.Done():
					t.Error("original cleanup server producer join deadline")
					break producerJoin
				}
			}
		}
		for _, resource := range []struct {
			name  string
			drain func(context.Context) error
		}{{"gate", f.gate.Drain}, {"body", f.r.bodyAdmission.Drain}, {"pipeline", f.r.exchanges.Drain}} {
			if err := resource.drain(ctx); err != nil {
				t.Error("original cleanup drain", resource.name, err)
			}
		}
		f.transport.CloseIdleConnections()
		f.listener.CloseClientConnections()
		f.upstream.CloseClientConnections()
		s := f.gate.Snapshot()
		if s.ActiveActions != 0 || s.EnteringActions != 0 || s.ActiveEgress != 0 || s.QueuedRequests != 0 || s.HeldBytes != 0 {
			t.Error("original cleanup retained admission", s)
		}
		t.Logf("original cleanup callers=%d joined=%t producers=%d gate=%+v", len(run.flights), run.allJoined(), run.producers, s)
	})
	return run
}

func (run *originalForwardRun) allJoined() bool {
	for _, flight := range run.flights {
		if !flight.joined {
			return false
		}
	}
	return true
}

func (run *originalForwardRun) start(t *testing.T) *originalForwardFlight {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	flight := &originalForwardFlight{done: make(chan capturedOriginalReply, 1), chunk: make(chan capturedOriginalReply, 1), cancel: cancel}
	run.flights = append(run.flights, flight)
	go func() {
		if run.f.cfg.family == "raw" {
			flight.done <- run.f.send(ctx, http.MethodGet, originalForwardRawPath, "")
			return
		}
		response, err := run.f.open(ctx, http.MethodPost, semanticOriginalPath, originalForwardBody)
		reply := capturedOriginalReply{Err: err}
		if response != nil {
			reply.Status, reply.Header = response.StatusCode, response.Header.Clone()
			reader := bufio.NewReader(response.Body)
			delta, observed := false, false
			for {
				line, readErr := reader.ReadBytes('\n')
				reply.Body = append(reply.Body, line...)
				if bytes.Contains(line, []byte(`"type":"response.output_text.delta"`)) {
					delta = true
				}
				if delta && !observed && bytes.Equal(line, []byte("\n")) {
					observed = true
					prefix := reply
					prefix.Body = bytes.Clone(reply.Body)
					flight.chunk <- prefix
				}
				if readErr != nil {
					if readErr != io.EOF {
						reply.Err = readErr
					}
					break
				}
			}
			_ = response.Body.Close()
		}
		flight.done <- reply
	}()
	return flight
}

func (flight *originalForwardFlight) join(t *testing.T) capturedOriginalReply {
	t.Helper()
	select {
	case reply := <-flight.done:
		flight.joined = true
		return reply
	case <-time.After(4 * time.Second):
		t.Fatal("original caller join deadline")
		return capturedOriginalReply{Err: errors.New("join deadline")}
	}
}

func (flight *originalForwardFlight) waitEntry(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case reply := <-flight.done:
		flight.joined = true
		t.Fatalf("original ended before checkpoint: %+v", reply)
	case <-time.After(2 * time.Second):
		t.Fatal("original checkpoint deadline")
	}
}

func (flight *originalForwardFlight) firstChunk(t *testing.T) capturedOriginalReply {
	t.Helper()
	select {
	case reply := <-flight.chunk:
		return reply
	case reply := <-flight.done:
		flight.joined = true
		t.Fatalf("original ended before downstream flushed delta: %+v", reply)
	case <-time.After(2 * time.Second):
		t.Fatal("original downstream chunk deadline")
	}
	return capturedOriginalReply{}
}

func (run *originalForwardRun) joinProducers(t *testing.T) {
	t.Helper()
	if run.f.producerDone == nil {
		return
	}
	for run.producers < run.f.httpCalls.Load() {
		select {
		case <-run.f.producerDone:
			run.producers++
		case <-time.After(2 * time.Second):
			t.Fatal("original server producer join deadline")
		}
	}
}

func assertOriginalForwardReply(t *testing.T, f *capturedOriginalFixture, reply capturedOriginalReply, prefix bool) {
	t.Helper()
	if reply.Err != nil {
		t.Fatalf("original reply status=%d err=%v body=%s", reply.Status, reply.Err, reply.Body)
	}
	assertOriginalForwardBody(t, f, reply, prefix)
}

func assertOriginalForwardBody(t *testing.T, f *capturedOriginalFixture, reply capturedOriginalReply, prefix bool) {
	t.Helper()
	if reply.Status != http.StatusOK {
		t.Fatalf("original body status=%d err=%v body=%s", reply.Status, reply.Err, reply.Body)
	}
	if f.cfg.family == "raw" {
		if reply.Header.Get("Content-Type") != "application/octet-stream" || reply.Header.Get("X-Original-Fixture") != "raw" || !bytes.Equal(reply.Body, []byte{0, 1, 2, 'r', 'a', 'w', 255}) {
			t.Fatalf("opaque query/header/raw byte fidelity: %+v", reply)
		}
		return
	}
	want := originalForwardPrefix
	if !prefix {
		want += originalForwardTerminal
	}
	if reply.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("original stream content type", reply.Header)
	}
	if f.cfg.family == "original_model" {
		if !bytes.Equal(reply.Body, []byte(want)) || reply.Header.Get("X-Original-Transform") != "" {
			t.Fatalf("untransformed original stream changed: %s headers=%v", reply.Body, reply.Header)
		}
	} else {
		for _, line := range bytes.Split(reply.Body, []byte("\n")) {
			if bytes.HasPrefix(line, []byte(":")) {
				t.Fatal("transformed original inserted/preserved comment or heartbeat", string(line))
			}
		}
		if reply.Header.Get("X-Original-Transform") != "response" || !bytes.Contains(reply.Body, []byte(`"delta":"transformed-delta"`)) || bytes.Contains(reply.Body, []byte("original-delta")) || bytes.Contains(reply.Body, []byte(": retained raw comment")) || bytes.Contains(reply.Body, []byte(": keepalive")) || !bytes.Contains(reply.Body, []byte("event: response.created\n")) || !bytes.Contains(reply.Body, []byte("event: response.output_text.delta\n")) || (!prefix && !bytes.Contains(reply.Body, []byte("event: response.completed\n"))) {
			t.Fatalf("published original transform/framing mismatch: %s headers=%v", reply.Body, reply.Header)
		}
	}
	if prefix && bytes.Contains(reply.Body, []byte("response.completed")) {
		t.Fatal("downstream checkpoint was after terminal event")
	}
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	events, err := decoder.Feed(reply.Body)
	if err != nil || decoder.Finish() != nil {
		t.Fatal("original invalid downstream SSE", err)
	}
	wantNames := []string{"response.created", "response.output_text.delta"}
	if !prefix {
		wantNames = append(wantNames, "response.completed")
	}
	if len(events) != len(wantNames) {
		t.Fatal("original extra/missing SSE events or heartbeat", events)
	}
	for i, event := range events {
		var payload struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				ID, Model, Status string
			} `json:"response"`
		}
		if event.Name != wantNames[i] || json.Unmarshal(event.Data, &payload) != nil || payload.Type != wantNames[i] {
			t.Fatal("original downstream event protocol mismatch", event)
		}
		if i == 1 {
			wantDelta := "original-delta"
			if f.cfg.family == "transformed" {
				wantDelta = "transformed-delta"
			}
			if payload.Delta != wantDelta {
				t.Fatal("original downstream semantic delta", payload)
			}
		} else {
			wantStatus := "in_progress"
			if i == 2 {
				wantStatus = "completed"
			}
			if payload.Response.ID != "original-forward" || payload.Response.Model != "synthetic" || payload.Response.Status != wantStatus {
				t.Fatal("original downstream response identity/completion", payload)
			}
		}
	}
	t.Logf("original stream downstream prefix=%t transformed=%t bytes=%d valid completion=%t", prefix, f.cfg.family == "transformed", len(reply.Body), !prefix)
}

func forwardTarget(f *capturedOriginalFixture, expected capturedOriginalExpected) offlinehold.ProbeTarget {
	policy := egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyDirect}, Resolver: egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverSystem, Transport: egressnetwork.ResolverTransportDirect}}
	proxyKind, proxyEndpoint := "direct", ""
	if expected.ProfileID == "profile.captured-original-a" {
		proxyKind, proxyEndpoint = "socks5", f.proxyA.Addr().String()
	} else if expected.ProfileID == "profile.captured-original-b" {
		proxyKind, proxyEndpoint = "socks5", f.proxyB.Addr().String()
	}
	if proxyKind == "socks5" {
		policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: proxyEndpoint}
	}
	want := offlinehold.ProbeTarget{Kind: offlinehold.EgressOpaque, Transport: offlinehold.ProbeTransportStrictTLS, TargetRef: "https://chatgpt.com", NetworkOrigin: "https://chatgpt.com", HTTPAuthority: "chatgpt.com", TLSServerName: "chatgpt.com", EgressPolicy: policy}
	if f.cfg.family == "raw" {
		// The raw request starts from the local test context with owner access.
		want.TargetAccess = egressnetwork.TargetAccessOwnerConfigured
	} else {
		// Independent NUL-delimited literal original provenance: no route.
		encoded := strings.Join([]string{"captured.original.egress", "1", f.assignment.EnvironmentDigest.String(), "original", "", "0", proxyKind, proxyEndpoint, "system", "", "direct"}, "\x00")
		digest := sha256.Sum256([]byte(encoded))
		want.Kind, want.TargetRef, want.PlanRevision, want.PlanDigest = offlinehold.EgressProvider, "original_destination.endpoint.test", 1, hex.EncodeToString(digest[:])
	}
	return want
}

func assertOriginalForwardTarget(t *testing.T, f *capturedOriginalFixture, targets []offlinehold.ProbeTarget) {
	t.Helper()
	want := forwardTarget(f, f.expected)
	if len(targets) != 1 || targets[0] != want || f.aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Kind != environment.DestinationKindOriginal || f.aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream != nil {
		t.Fatalf("original frozen target=%+v want=%+v", targets, want)
	}
	if f.cfg.family != "raw" {
		f.mu.Lock()
		requests := append([]providertransport.Request(nil), f.semanticRequests...)
		f.mu.Unlock()
		if len(requests) != 1 || requests[0].ProbeTarget() != want {
			t.Fatal("original held target differs from actual Exchange request", requests)
		}
		assertOriginalRequestAuthority(t, requests[0])
	}
	if f.secrets.reads.Load() != 0 || f.secrets.inspects.Load() != 0 || f.secrets.writes.Load() != 0 || f.network.tokenAttempts.Load() != 0 {
		t.Fatal("held original request acquired managed credential effects")
	}
	t.Logf("original queued family=%s actual concrete probe target=%+v HTTP=0", f.cfg.family, targets[0])
}

func assertOriginalRequestAuthority(t *testing.T, request providertransport.Request) {
	t.Helper()
	account, managed := request.AccountRef()
	if managed || !reflect.DeepEqual(account, providerauth.AccountRef{}) || request.CredentialMode() != providerauth.CredentialClientPassthrough || request.TargetRef() != "original_destination.endpoint.test" {
		t.Fatal("original destination acquired route/managed authority", account, managed, request.TargetRef())
	}
}

func (f *capturedOriginalFixture) assertForwardAudits(t *testing.T, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if want > 0 {
		if err := f.r.exchanges.Drain(ctx); err != nil {
			t.Fatal("original audit Runtime consumer drain", err)
		}
	}
	page, err := f.r.EgressAttempts().List(ctx, egressaudit.PageRequest{Limit: 100})
	if err != nil || len(page.Items) != want {
		t.Fatalf("original audits=%d want=%d err=%v", len(page.Items), want, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	wantRequests := want
	if f.cfg.family == "raw" {
		wantRequests = 0
	} else if want == 0 {
		// The sole Exchange request exists before Hold admission, even when
		// cancellation or a failed probe correctly emits no egress attempt.
		wantRequests = 1
	}
	if len(f.starts) != want || len(f.wants) != want || len(f.semanticRequests) != wantRequests {
		t.Fatal("original orphan/replayed request", len(f.starts), len(f.wants), len(f.semanticRequests), want)
	}
	parents, exchanges := map[string]bool{}, map[string]bool{}
	for _, record := range page.Items {
		a, d := record.Attempt, record.Attempt.Decision()
		expected, ok := f.wants[a.ID()]
		if !ok || !a.Terminal() || d != f.starts[a.ID()] || d.SelectionSource != expected.Source || d.ProxyID != expected.ProfileID || d.ProxyRevision != 1 || d.DefaultRevision != expected.DefaultRevision || d.AccountSettingsRevision != 0 || d.AccountID != "" || a.TargetOrigin() != "https://chatgpt.com" {
			t.Fatalf("original immutable attempt id=%s decision=%+v start=%+v expected=%+v", a.ID(), d, f.starts[a.ID()], expected)
		}
		parent := a.Parent()
		if parent.ID == "" || parent.ID == a.ID() || parents[parent.ID] || a.ConnectionID() == "" {
			t.Fatal("original independent typed parent/connection missing", parent)
		}
		parents[parent.ID] = true
		timeline, err := f.r.ConnectionEvents().Timeline(ctx, a.ConnectionID())
		bound := false
		for _, record := range timeline.Events {
			e := record.Event
			if e.ConnectionID == a.ConnectionID() && e.EnvironmentID == f.assignment.EnvironmentID && e.EnvironmentRevision == f.assignment.EnvironmentRevision {
				bound = true
			}
		}
		if err != nil || !bound {
			t.Fatal("original audit connection has no captured Environment authority", err, timeline)
		}
		failed := f.cfg.edge == "proxy_loss" || f.cfg.edge == "target_dial_failure" || f.cfg.edge == "strict_tls_failure"
		if f.cfg.family == "raw" {
			policyID, revision, rule := expected.ProfileID, uint64(1), "flow.egress"
			if expected.Source == egressprofile.SelectionGlobal || expected.Source == egressprofile.SelectionBuiltin {
				policyID, revision, rule = "runtime.egress-default", expected.DefaultRevision, "global.default"
			}
			if a.Purpose() != egressaudit.PurposeOriginalOrigin || a.PayloadClass() != egressaudit.PayloadControl || d.Authority != egressaudit.AuthorityNetwork || d.PolicyID != policyID || d.PolicyRevision != revision || d.RuleID != rule || parent.Kind != egressaudit.ParentOriginalRequest || parent.ExchangeID != "" {
				t.Fatal("opaque original transport audit lost actual network provenance", a)
			}
		} else {
			if a.Purpose() != egressaudit.PurposeProviderAttempt || a.PayloadClass() != egressaudit.PayloadClientSemantic || d.Authority != egressaudit.AuthorityEnvironment || d.PolicyID != "captured.original.egress" || d.PolicyRevision != 1 || d.RuleID != "original.destination" || parent.Kind != egressaudit.ParentUpstreamAttempt || parent.ExchangeID == "" || exchanges[parent.ExchangeID] {
				t.Fatal("original provider audit fabricated upstream route", a)
			}
			exchanges[parent.ExchangeID] = true
			boundRequests := 0
			for _, request := range f.semanticRequests {
				if request.EgressAttemptID() == a.ID() {
					assertOriginalRequestAuthority(t, request)
					if request.RequestID() != parent.ID || request.ParentAttemptID() != parent.ID || request.ExchangeID() != parent.ExchangeID || request.ConnectionID() != a.ConnectionID() || request.ProbeTarget() != forwardTarget(f, expected) {
						t.Fatal("original audit parent/target differs from actual Exchange-created Request", parent)
					}
					boundRequests++
				}
			}
			if boundRequests != 1 {
				t.Fatal("original parent has no unique actual Exchange-created Request", boundRequests)
			}
			exchange, err := f.r.Activities().GetExchange(ctx, parent.ExchangeID)
			if err != nil || exchange.Kind != activity.KindExchangeCompleted || exchange.SubjectID != parent.ExchangeID || exchange.ConnectionID != a.ConnectionID() || exchange.EnvironmentID != "captured.original.egress" || exchange.EnvironmentRevision != 1 || exchange.EnvironmentDigest != f.assignment.EnvironmentDigest.String() || exchange.RouteID != "" || exchange.RouteRevision != 0 || exchange.AccountID != "" || exchange.AccountRevision != 0 || exchange.CredentialEpoch != 0 || exchange.ManualCaptureID != f.capture.ID {
				t.Fatalf("original actual persisted Exchange linkage: parent=%+v exchange=%+v err=%v", parent, exchange, err)
			}
			if f.cfg.edge == "stream_disconnect" {
				if exchange.Status != activity.StatusCanceled && exchange.Status != activity.StatusFailed {
					t.Fatal("disconnected original Exchange claimed success", exchange.Status)
				}
			} else if (failed && exchange.Status != activity.StatusFailed) || (!failed && exchange.Status != activity.StatusSucceeded) {
				t.Fatal("original Exchange terminal status", exchange.Status)
			}
		}
		if f.cfg.edge == "stream_disconnect" {
			if a.Outcome() != egressaudit.OutcomeFailed && a.Outcome() != egressaudit.OutcomeCanceled {
				t.Fatal("disconnected original egress claimed success", a.Outcome())
			}
		} else if (failed && a.Outcome() != egressaudit.OutcomeFailed) || (!failed && a.Outcome() != egressaudit.OutcomeCompleted) {
			t.Fatal("original egress terminal outcome", a.Outcome())
		}
		if !f.asserted[a.ID()] {
			t.Logf("original family=%s attempt=%s parent=%+v connection=%s purpose=%s outcome=%s decision=%+v", f.cfg.family, a.ID(), parent, a.ConnectionID(), a.Purpose(), a.Outcome(), d)
			f.asserted[a.ID()] = true
		}
	}
}
