//go:build darwin

package main

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/activity"
	"os"
	"testing"
	"time"
)

func modelPinnedThreadParams(dir, version string, idle int) map[string]any {
	params := modelIdleThreadParams(dir, idle)
	if version == "0.145.0" {
		delete(params["config"].(map[string]any), "tools.update_plan.enabled")
	}
	return params
}

func TestModelSSEEachPinnedNativeHeldStream(t *testing.T) {
	version := os.Getenv("VIBERMATE_NATIVE_VERSION")
	if version == "" {
		t.Skip("requires exact MODEL client version; not acceptance evidence")
	}
	parent, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary, node := modelIdleVerifiedClient(t, parent, t.TempDir(), version)
	trace, client := &modelIdleTrace{}, &modelIdleClientObservation{}
	options := &nativeModelProbeOptions{Handler: modelIdleProvider(trace, "managed_long_arguments"), ThreadParameters: func(dir string) map[string]any { return modelPinnedThreadParams(dir, version, 20000) }, ClientObservation: client, ExpectedModelRequests: 2,
		ObserveTerminal: func(ctx context.Context, actual nativeModelProbeObservation) {
			evidence := collectModelLifecycleEvidence(t, ctx, actual)
			trace.mu.Lock()
			requests, acks, failure, interval := trace.requests, trace.acks, trace.failure, time.Since(trace.first)
			trace.mu.Unlock()
			client.mu.Lock()
			plans, idle := client.plans, client.idle
			client.mu.Unlock()
			for _, record := range evidence.records {
				if record.Status != activity.StatusSucceeded {
					t.Error("fixed native pin lacks succeeded Activity")
				}
			}
			t.Logf("fixed native MODEL version=%s completed=%t requests=%d plan=%d ack=%d generatedDATA=%d toolBatches=%d args=%d intervalMS=%d idle=%t", version, actual.Completed, requests, plans, acks, evidence.generatedData, evidence.toolBatches, evidence.argumentEvents, interval.Milliseconds(), idle)
			if !actual.Completed || requests != 2 || acks != 1 || plans != 1 || idle || failure != "" || evidence.generatedData != 2 || evidence.comments != 0 || evidence.toolBatches != 1 || evidence.argumentEvents != 20 {
				t.Error("fixed native pin healthy held stream not proved")
			}
		}}
	runNativeFixtureSliceWithModelProbe(modelIdleManagedChecks{T: t, ctx: parent, version: version}, binary, options, node)
}

func TestModelSSEEachPinEffectiveIdle(t *testing.T) {
	version := os.Getenv("VIBERMATE_NATIVE_VERSION")
	if version == "" {
		t.Skip("requires exact MODEL client version; not acceptance evidence")
	}
	parent, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary, node := modelIdleVerifiedClient(t, parent, t.TempDir(), version)
	trace, client := &modelIdleTrace{}, &modelIdleClientObservation{}
	options := &nativeModelProbeOptions{Handler: modelIdleProvider(trace, "managed_silence"), ThreadParameters: func(dir string) map[string]any { return modelPinnedThreadParams(dir, version, 180) }, ClientObservation: client, ExpectedModelRequests: 1,
		ObserveTerminal: func(ctx context.Context, actual nativeModelProbeObservation) {
			evidence := collectModelLifecycleEvidence(t, ctx, actual)
			trace.mu.Lock()
			requests, events, acks, failure, interval := trace.requests, trace.events, trace.acks, trace.failure, time.Since(trace.first)
			trace.mu.Unlock()
			client.mu.Lock()
			idle, plans := client.idle, client.plans
			client.mu.Unlock()
			t.Logf("actual fixed idle version=%s intervalMS=%d clientIdle=%t completed=%t requests=%d upstreamFlushes=%d generatedDATA=%d", version, interval.Milliseconds(), idle, actual.Completed, requests, events, evidence.generatedData)
			if actual.Completed || !idle || requests != 1 || events != 2 || acks != 0 || plans != 0 || failure != "" || interval < 150*time.Millisecond || interval > 2*time.Second || evidence.generatedData != 0 || evidence.hasTool || evidence.hasCompleted {
				t.Error("exact client active-provider idle/retries0 not established")
			}
		}}
	runNativeFixtureSliceWithModelProbe(modelIdleManagedChecks{T: t, ctx: parent, version: version}, binary, options, node)
}
