//go:build darwin

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

type modelIdleManagedChecks struct{ *testing.T }

func (modelIdleManagedChecks) NativeScenario() string        { return "model_probe" }
func (modelIdleManagedChecks) NativeExpectedVersion() string { return "0.160.0" }

// Desired behavior, intentionally RED if healthy provider argument events are
// held until after the real client's event-idle deadline. No product timers or
// decisions are changed; the synthetic route uses the real managed pipeline.
func TestModelSSEIdleManagedHeldArgumentsComplete(t *testing.T) {
	runModelIdleManaged(t, "long_arguments")
}

func TestModelSSEIdleManagedControls(t *testing.T) {
	// The pre-fix long_silence client-idle observation is retained in report
	// 47896. Local DATA now keeps it live; actual server5m proof is Task2.
	for _, mode := range []string{"long_keepalive", "text"} {
		t.Run(mode, func(t *testing.T) { runModelIdleManaged(t, mode) })
	}
}

func runModelIdleManaged(t *testing.T, mode string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary, node := modelIdleVerifiedClient(t, ctx, t.TempDir())
	trace, client := &modelIdleTrace{}, &modelIdleClientObservation{}
	expectedRequests := 2
	if mode == "text" {
		expectedRequests = 1
	}
	options := &nativeModelProbeOptions{
		Handler:               modelIdleProvider(trace, "managed_"+mode),
		ThreadParameters:      func(dir string) map[string]any { return modelIdleThreadParams(dir, 20000) },
		ClientObservation:     client,
		ExpectedModelRequests: expectedRequests,
		ObserveTerminal: func(ctx context.Context, actual nativeModelProbeObservation) {
			var records activity.Page
			deadline := time.Now().Add(5 * time.Second)
			for {
				var err error
				records, err = actual.Runtime.Activities().ListExchanges(ctx, activity.PageRequest{CaptureRunID: actual.Capture.ID, Limit: 10})
				if err != nil {
					t.Fatal("real Activity read failed")
				}
				terminal := len(records.Items) == actual.ModelRequests
				for _, record := range records.Items {
					terminal = terminal && record.Kind == activity.KindExchangeCompleted
				}
				if terminal || time.Now().After(deadline) {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("Activity bound expired")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if len(records.Items) != actual.ModelRequests || len(records.Items) == 0 {
				t.Fatal("actual model Activity missing")
			}
			client.mu.Lock()
			idle, plans := client.idle, client.plans
			client.mu.Unlock()
			trace.mu.Lock()
			requests, events, acks, failure := trace.requests, trace.events, trace.acks, trace.failure
			interval := time.Since(trace.first)
			trace.mu.Unlock()
			comments, downstreamData, argumentEvents := 0, 0, 0
			generatedData, toolBatches, heldArguments := 0, 0, 0
			toolExposed := false
			for _, record := range records.Items {
				ownership := record.CaptureRunID == actual.Capture.ID && record.EnvironmentID == actual.Assignment.EnvironmentID.String() && record.EnvironmentRevision == uint64(actual.Assignment.EnvironmentRevision) && record.EnvironmentDigest == actual.Assignment.EnvironmentDigest.String() && record.AccountID == actual.AccountID
				if !ownership || record.Kind != activity.KindExchangeCompleted {
					t.Fatal("actual terminal ownership differs")
				}
				if actual.Completed && record.Status != activity.StatusSucceeded {
					t.Error("completed control lacks succeeded real Activity")
				}
				if record.Transport == nil || record.Transport.Presentation == nil || record.Transport.Presentation.ClientProtocol != "h2" || record.Transport.Presentation.UpstreamProtocol != "http/1.1" || record.Transport.Presentation.ProtocolAdjustment != "cleartext_http1" {
					t.Fatal("actual managed transport presentation missing")
				}
				envelopes, err := actual.Runtime.RawEvidence().ListExchange(ctx, record.SubjectID)
				if err != nil {
					t.Fatal("actual raw envelope metadata unavailable")
				}
				for _, meta := range envelopes {
					if meta.Layer != rawevidence.LayerClientDownstream && meta.Layer != rawevidence.LayerProviderResponse {
						continue
					}
					// Only private synthetic response payloads; never dump headers or bodies.
					raw, err := actual.Runtime.RawEvidence().Reveal(ctx, rawevidence.RevealRequest{EnvelopeID: meta.EnvelopeID, ActorID: "synthetic-idle-diagnostic"})
					if err != nil {
						t.Fatal("synthetic Full response evidence unavailable")
					}
					if meta.Layer == rawevidence.LayerClientDownstream {
						for _, frame := range raw.Payload.Frames {
							if frame.Kind == rawevidence.FrameKeepalive {
								wire := raw.Payload.Body[frame.Offset : frame.Offset+frame.Length]
								if bytes.Equal(wire, []byte("event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n")) {
									generatedData++
								} else if bytes.Equal(wire, []byte(": keepalive\n\n")) {
									comments++
								} else {
									t.Error("generated keepalive carried unexpected content")
								}
							}
							if frame.Kind == rawevidence.FrameData {
								downstreamData++
								wire := raw.Payload.Body[frame.Offset : frame.Offset+frame.Length]
								if bytes.Contains(wire, []byte("function_call")) {
									toolBatches++
									heldArguments += bytes.Count(wire, []byte(`"type":"response.function_call_arguments.delta"`))
									if !bytes.Contains(wire, []byte(`"type":"response.completed"`)) {
										t.Error("tool bytes escaped before terminal release batch")
									}
								}
							}
						}
						toolExposed = toolExposed || bytes.Contains(raw.Payload.Body, []byte("function_call"))
					} else {
						argumentEvents += bytes.Count(raw.Payload.Body, []byte(`"type":"response.function_call_arguments.delta"`))
					}
				}
				t.Logf("actual Activity terminal=%s status=%s reason=%s ownership=true rawEnvelopes=%d", record.Kind, record.Status, record.ReasonCode, len(envelopes))
			}
			t.Logf("managed160 mode=%s completed=%t failure=%s idle=%t modelRequests=%d flushedData=%d upstreamArguments=%d downstreamDataFrames=%d actualCommentFrames=%d generatedDataKeepalive=%d toolBatches=%d heldArgumentEvents=%d plans=%d ack=%d toolExposed=%t intervalMS=%d fixtureFailure=%s", mode, actual.Completed, actual.Failure, idle, requests, events, argumentEvents, downstreamData, comments, generatedData, toolBatches, heldArguments, plans, acks, toolExposed, interval.Milliseconds(), failure)
			if failure != "" || requests != actual.ModelRequests {
				t.Fatal("managed fixture invalid")
			}
			if mode == "text" {
				if !actual.Completed || idle || events != 27 || plans != 0 || acks != 0 || toolExposed {
					t.Error("actual safe text control differs")
				}
				return
			}
			if mode == "long_keepalive" {
				if !actual.Completed || idle || requests != 2 || events != 27 || plans != 1 || acks != 1 || !toolExposed {
					t.Error("actual native data keepalive control differs")
				}
				return
			}
			if !actual.Completed {
				if !idle || interval < 19*time.Second || interval > 25*time.Second || argumentEvents < 10 || comments < 1 || toolExposed || plans != 0 || acks != 0 {
					t.Fatal("failed turn does not establish target progressing-stream/comment mechanism")
				}
			}
			if !actual.Completed {
				t.Error("healthy continuously progressing model arguments must complete without releasing unapproved tool data")
			} else if idle || requests != 2 || plans != 1 || acks != 1 || generatedData != 2 || comments != 0 || toolBatches != 1 || heldArguments != 20 {
				t.Error("healthy target lost generated nonsemantic liveness or atomic approved tool release")
			}
		},
	}
	runNativeFixtureSliceWithModelProbe(modelIdleManagedChecks{t}, binary, options, node)
}
