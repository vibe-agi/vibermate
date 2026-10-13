//go:build darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestModelProbePrivateLifetimeAndProfileBounds(t *testing.T) {
	valid := nativeModelProbeOptions{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), ThreadParameters: func(string) map[string]any { return nil }, ObserveTerminal: func(context.Context, nativeModelProbeObservation) {}, ExpectedModelRequests: 1}
	for _, test := range []struct {
		name, scenario string
		edit           func(*nativeModelProbeOptions)
		want           time.Duration
		invalid        bool
	}{
		{name: "default", scenario: "model_probe", want: 60 * time.Second},
		{name: "long_cap", scenario: "model_probe", edit: func(p *nativeModelProbeOptions) { p.WorkLimit = 330 * time.Second }, want: 330 * time.Second},
		{name: "negative", scenario: "model_probe", edit: func(p *nativeModelProbeOptions) { p.WorkLimit = -time.Second }, invalid: true},
		{name: "over_cap", scenario: "model_probe", edit: func(p *nativeModelProbeOptions) { p.WorkLimit = 331 * time.Second }, invalid: true},
		{name: "native_scenario", scenario: "observe", invalid: true},
		{name: "unknown_profile", scenario: "model_probe", edit: func(p *nativeModelProbeOptions) { p.Profile = 255 }, invalid: true},
		{name: "expiry_without_review", scenario: "model_probe", edit: func(p *nativeModelProbeOptions) { p.ExpireReview = true }, invalid: true},
		{name: "bounded_real_expiry", scenario: "model_probe", edit: func(p *nativeModelProbeOptions) { p.ExpireReview = true; p.Review = true }, want: 60 * time.Second},
		{name: "zero_model_not_success", scenario: "model_probe", edit: func(p *nativeModelProbeOptions) { p.ExpectedModelRequests = 0 }, invalid: true},
		{name: "missing_key_negative", scenario: "model_probe", edit: func(p *nativeModelProbeOptions) {
			p.Profile = nativeModelProbeAPIKeyMissing
			p.ExpectedModelRequests = 0
		}, want: 60 * time.Second},
		{name: "empty_key_negative", scenario: "model_probe", edit: func(p *nativeModelProbeOptions) { p.Profile = nativeModelProbeAPIKeyEmpty; p.ExpectedModelRequests = 0 }, want: 60 * time.Second},
		{name: "no_handler", scenario: "model_probe", edit: func(p *nativeModelProbeOptions) { p.Handler = nil }, invalid: true},
		{name: "no_observer", scenario: "model_probe", edit: func(p *nativeModelProbeOptions) { p.ObserveTerminal = nil }, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := valid
			if test.edit != nil {
				test.edit(&probe)
			}
			limit, err := modelProbeWorkLimit(test.scenario, &probe)
			if (err != nil) != test.invalid || !test.invalid && limit != test.want {
				t.Fatalf("limit=%s err=%v", limit, err)
			}
		})
	}
	if limit, err := modelProbeWorkLimit("observe", nil); err != nil || limit != 60*time.Second {
		t.Fatal("nil helper lifetime changed")
	}
	parent, cancel := context.WithTimeout(context.Background(), 360*time.Second)
	checks := modelIdleManagedChecks{T: t, ctx: parent}
	child, closeChild := context.WithTimeout(checks.NativeContext(), 330*time.Second)
	defer closeChild()
	if checks.NativeContext() != parent {
		t.Fatal("outer MODEL parent was discarded")
	}
	deadline, _ := child.Deadline()
	if left := time.Until(deadline); left > 330*time.Second || left < 329*time.Second {
		t.Fatal("private work/outer bounds differ")
	}
	cancel()
	if !errors.Is(child.Err(), context.Canceled) {
		t.Fatal("caller cancellation did not reach fixture work")
	}
}

type modelLifecycleEvidence struct {
	records                                                          []activity.Record
	raw                                                              map[string][]rawevidence.RevealedEnvelope
	generatedData, comments, dataFrames, toolBatches, argumentEvents int
	hasTool, hasCompleted, hasFailed                                 bool
}

func collectModelLifecycleEvidence(t *testing.T, ctx context.Context, actual nativeModelProbeObservation) modelLifecycleEvidence {
	t.Helper()
	var result modelLifecycleEvidence
	snapshot, err := runtimeModelSnapshot(ctx, actual)
	if err != nil {
		t.Fatal("complete lifecycle snapshot unavailable: ", err)
	}
	result.records = snapshot.records
	result.raw = snapshot.raw
	if len(result.records) != actual.ModelRequests {
		t.Fatal("lifecycle request/Activity count differs")
	}
	for _, record := range result.records {
		if record.Kind != activity.KindExchangeCompleted || record.CaptureRunID != actual.Capture.ID || record.EnvironmentID != actual.Assignment.EnvironmentID.String() || record.EnvironmentRevision != uint64(actual.Assignment.EnvironmentRevision) || record.EnvironmentDigest != actual.Assignment.EnvironmentDigest.String() || record.AccountID != actual.AccountID || record.AccountRevision != 1 {
			t.Fatal("lifecycle ownership or terminal mismatch")
		}
		if record.Transport == nil || record.Transport.Presentation == nil || record.Transport.Presentation.ClientProtocol != "h2" || record.Transport.Presentation.UpstreamProtocol != "http/1.1" || record.Transport.Presentation.ProtocolAdjustment != "cleartext_http1" {
			t.Fatal("actual private managed transport evidence differs")
		}
		metadata := snapshot.raw[record.SubjectID]
		for _, raw := range metadata {
			meta := raw.Metadata
			if meta.Layer != rawevidence.LayerClientDownstream && meta.Layer != rawevidence.LayerProviderResponse {
				continue
			}
			if meta.Layer == rawevidence.LayerProviderResponse {
				result.argumentEvents += bytes.Count(raw.Payload.Body, []byte(`"type":"response.function_call_arguments.delta"`))
				continue
			}
			result.hasTool = result.hasTool || bytes.Contains(raw.Payload.Body, []byte("function_call"))
			result.hasCompleted = result.hasCompleted || bytes.Contains(raw.Payload.Body, []byte("response.completed"))
			result.hasFailed = result.hasFailed || bytes.Contains(raw.Payload.Body, []byte("response.failed"))
			for _, frame := range raw.Payload.Frames {
				wire := raw.Payload.Body[frame.Offset : frame.Offset+frame.Length]
				if frame.Kind == rawevidence.FrameKeepalive {
					if bytes.Equal(wire, []byte("event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n")) {
						result.generatedData++
					} else if bytes.Equal(wire, []byte(": keepalive\n\n")) {
						result.comments++
					} else {
						t.Fatal("lifecycle keepalive has unexpected payload")
					}
				}
				if frame.Kind == rawevidence.FrameData {
					result.dataFrames++
					if bytes.Contains(wire, []byte("function_call")) {
						result.toolBatches++
					}
				}
			}
		}
		t.Logf("lifecycle Activity kind=%s status=%s reason=%s ownership=true rawEnvelopes=%d", record.Kind, record.Status, record.ReasonCode, len(metadata))
	}
	return result
}

func TestModelSSEDefaultRuntimeProviderIdle(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 360*time.Second)
	defer cancel()
	binary, node := modelIdleVerifiedClient(t, parent, t.TempDir())
	trace, client := &modelIdleTrace{}, &modelIdleClientObservation{}
	stopped := make(chan struct{})
	var stopOnce sync.Once
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer stopOnce.Do(func() { close(stopped) })
		if r.Method != "POST" || r.TLS != nil || r.ProtoMajor != 1 || r.Header.Get("Authorization") != "Bearer synthetic-managed-model" {
			trace.fail("default_idle_transport_or_account")
			w.WriteHeader(400)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
		if err != nil || len(body) > 4<<20 {
			trace.fail("default_idle_request_bound")
			w.WriteHeader(400)
			return
		}
		codec, err := openairesponses.New(openairesponses.DefaultOptions())
		if err != nil {
			trace.fail("default_idle_codec")
			return
		}
		if _, _, err = codec.DecodeCompatibleClientRequest(body); err != nil {
			trace.fail("default_idle_actual_request_invalid")
			w.WriteHeader(400)
			return
		}
		trace.mu.Lock()
		trace.requests++
		requests := trace.requests
		trace.mu.Unlock()
		if requests != 1 {
			trace.fail("default_idle_request_retried")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		modelIdleWrite(w, trace, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_default_idle", "created_at": 1, "model": "gpt-5.4", "status": "in_progress", "output": []any{}}})
		// No35/60/90s terminal and no upstream heartbeat: only the actual
		// production transport/Pipeline or owner cancellation may close this.
		<-r.Context().Done()
	})
	options := &nativeModelProbeOptions{WorkLimit: 330 * time.Second, Handler: handler, ThreadParameters: func(dir string) map[string]any { return modelIdleThreadParams(dir, 20000) }, ClientObservation: client, ExpectedModelRequests: 1,
		ObserveTerminal: func(work context.Context, actual nativeModelProbeObservation) {
			workLive := work.Err() == nil && parent.Err() == nil
			evidence := collectModelLifecycleEvidence(t, work, actual)
			trace.mu.Lock()
			elapsed, requests, events, failure := time.Since(trace.first), trace.requests, trace.events, trace.failure
			trace.mu.Unlock()
			client.mu.Lock()
			idle, plans := client.idle, client.plans
			client.mu.Unlock()
			select {
			case <-stopped:
			case <-time.After(2 * time.Second):
				t.Fatal("default idle provider request did not drain")
			}
			t.Logf("defaultRuntimeIdle elapsedMS=%d workLive=%t completed=%t clientIdle=%t requests=%d upstreamFlushes=%d generatedDATA=%d comments=%d plans=%d failedFrame=%t fixtureFailure=%s", elapsed.Milliseconds(), workLive, actual.Completed, idle, requests, events, evidence.generatedData, evidence.comments, plans, evidence.hasFailed, failure)
			if !workLive || actual.Completed || idle || elapsed < 295*time.Second || elapsed > 315*time.Second || requests != 1 || events != 1 || failure != "" || len(evidence.records) != 1 || evidence.records[0].ReasonCode != "provider_response_idle" || evidence.records[0].Status == activity.StatusSucceeded || evidence.generatedData < 18 || evidence.generatedData > 21 || evidence.comments != 0 || evidence.hasTool || evidence.hasCompleted || !evidence.hasFailed || plans != 0 {
				t.Error("real unchanged5m server idle termination not proved")
			}
		}}
	runNativeFixtureSliceWithModelProbe(modelIdleManagedChecks{T: t, ctx: parent}, binary, options, node)
}
