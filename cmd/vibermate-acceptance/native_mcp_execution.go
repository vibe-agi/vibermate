package main

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// Private runner assertions abort one case while unwinding every owned
// resource. Failure text is never copied into the fixed sanitized report.
type nativeCaseAbort struct{ stage string }
type nativeExecutionChecks struct {
	ctx       context.Context
	directory string
	mu        sync.Mutex
	failed    bool
	cleanup   []func()
	evidence  nativeMCPCaseReport
	logf      func(string, ...any)
}

func (*nativeExecutionChecks) Helper() {}
func (checks *nativeExecutionChecks) Logf(format string, values ...any) {
	if checks.logf != nil {
		checks.logf(format, values...)
	}
}
func (*nativeExecutionChecks) Fatal(values ...any) {
	stage := "fixture_dependency_failed"
	if len(values) > 0 {
		if text, ok := values[0].(string); ok {
			stage = text
		}
	}
	panic(nativeCaseAbort{stage: stage})
}
func (*nativeExecutionChecks) Fatalf(format string, values ...any) {
	panic(nativeCaseAbort{stage: format})
}
func (checks *nativeExecutionChecks) Error(...any) {
	checks.mu.Lock()
	checks.failed = true
	checks.mu.Unlock()
}
func (checks *nativeExecutionChecks) Cleanup(fn func()) {
	checks.mu.Lock()
	checks.cleanup = append(checks.cleanup, fn)
	checks.mu.Unlock()
}
func (checks *nativeExecutionChecks) TempDir() string {
	directory, err := os.MkdirTemp(checks.directory, "case-*")
	if err != nil {
		checks.Fatal()
	}
	checks.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}
func (checks *nativeExecutionChecks) NativeContext() context.Context { return checks.ctx }
func (checks *nativeExecutionChecks) NativeExpectedVersion() string  { return checks.evidence.Client }
func (checks *nativeExecutionChecks) NativeScenario() string         { return checks.evidence.Name }
func (checks *nativeExecutionChecks) NativeCounters(native, model int) {
	checks.evidence.NativeOutbound = native
	checks.evidence.ModelOutbound = model
}
func (checks *nativeExecutionChecks) NativeOwnership(capture, environment, activity bool) {
	checks.evidence.CaptureMatched = capture
	checks.evidence.EnvironmentMatched = environment
	checks.evidence.ActivityMatched = activity
}

func runNativeObserveCase(ctx context.Context, directory string, client nativePinnedClient, node string) (result nativeMCPCaseReport, resultErr error) {
	return runNativePolicyCase(ctx, directory, client, node, "observe")
}

func runNativePolicyCase(ctx context.Context, directory string, client nativePinnedClient, node, scenario string, logs ...func(string, ...any)) (result nativeMCPCaseReport, resultErr error) {
	if _, available := nativeAcceptanceContentProfiles(); nativeProfileRequired(scenario) && !available {
		return nativeMCPCaseReport{Name: scenario, Client: client.Version, Outcome: "incomplete", Reason: "synthetic_content_profiles_required"}, fmt.Errorf("synthetic_content_profiles_required")
	}
	checks := &nativeExecutionChecks{ctx: ctx, directory: directory, evidence: nativeMCPCaseReport{Name: scenario, Client: client.Version, Outcome: "incomplete", Reason: "not_started"}}
	if len(logs) > 0 {
		checks.logf = logs[0]
	}
	stage := "fixture_assertion_failed"
	defer func() {
		if recovered := recover(); recovered != nil {
			failure, expected := recovered.(nativeCaseAbort)
			if !expected {
				panic(recovered)
			}
			stage = failure.stage
			checks.failed = true
		}
		for i := len(checks.cleanup) - 1; i >= 0; i-- {
			checks.cleanup[i]()
		}
		result = checks.evidence
		// This named native-only scenario proves policy execution and absence
		// of model Exchanges, not ownership of a nonexistent model Activity.
		validCounts := result.ModelOutbound == 0
		switch scenario {
		case "observe", "review_allow_once":
			validCounts = validCounts && result.NativeOutbound >= 4
		case "off":
			validCounts = validCounts && result.NativeOutbound == 0
		case "strict_external":
			validCounts = validCounts && result.NativeOutbound >= 3
		case "review_reject", "review_deny_known", "review_sensitive_proof", "review_sensitive_credential":
			validCounts = validCounts && result.NativeOutbound >= 3
		case "session404_once":
			validCounts = validCounts && result.NativeOutbound >= 4
		case "sse_reconnect", "sse_observe":
			validCounts = validCounts && result.NativeOutbound == 9
		case "model_probe":
			validCounts = result.ModelOutbound == 1 && result.ActivityMatched
		default:
			_, probe := nativeProfileProbeFor(scenario)
			validCounts = validCounts && probe && (result.NativeOutbound == 4 || result.NativeOutbound == 5)
		}
		if checks.failed || !result.CaptureMatched || !result.EnvironmentMatched || !validCounts {
			result.Outcome = "failed"
			result.Reason = "native_case_failed"
			resultErr = fmt.Errorf("native policy case failed: %s", stage)
			return
		}
		result.Outcome = "passed"
		result.Reason = "completed_native_only"
		if scenario == "off" {
			result.Reason = "permission_off_zero_outbound"
		}
		if scenario == "strict_external" {
			result.Reason = "strict_external_zero_call"
		}
		if scenario == "review_reject" || scenario == "review_deny_known" || scenario == "review_sensitive_proof" || scenario == "review_sensitive_credential" {
			result.Reason = "review_rejected_zero_call"
		}
		if scenario == "review_allow_once" {
			result.Reason = "synthetic_review_allowed_once"
		}
		if scenario == "session404_once" {
			result.Reason = "execution_uncertain_not_replayed"
		}
		if scenario == "sse_reconnect" {
			result.Reason = "sse_resumed_without_call_replay"
		}
		if scenario == "sse_observe" {
			result.Reason = "sse_observe_only_no_review_evidence"
		}
		if scenario == "model_probe" {
			result.Reason = "actual_model_activity"
		}
		if _, probe := nativeProfileProbeFor(scenario); probe {
			result.Reason = "synthetic_full_diagnostic_only"
		}
	}()
	runNativeFixtureSlice(checks, client.invocation, node)
	return
}
