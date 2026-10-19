//go:build vibermate_acceptance_synthetic

package main

import (
	"context"
	"os"
	"testing"
)

// Diagnostic only: every invocation selects one independently verified exact
// pin. These observations never substitute for the fixed standalone46 rows.
func TestNativeUnsentPinnedStartupAndDecisions(t *testing.T) {
	binary, node, version := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE"), os.Getenv("VIBERMATE_NATIVE_VERSION")
	if binary == "" || node == "" || version == "" {
		t.Skip("exact client/version/Node required; not acceptance evidence")
	}
	switch version {
	case "0.145.0", "0.159.2", "0.160.0":
	default:
		t.Fatal("unsupported exact diagnostic pin")
	}
	for _, name := range []string{"observe", "review_allow_once", "review_reject", "review_deny_known", "review_sensitive_proof", "review_sensitive_credential"} {
		t.Run(name, func(t *testing.T) {
			result, err := runNativePolicyCase(context.Background(), t.TempDir(), nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: version}, invocation: binary}, node, name, t.Logf)
			if err != nil || result.Outcome != "passed" || result.ModelOutbound != 0 || !result.CaptureMatched || !result.EnvironmentMatched || result.ActivityMatched {
				t.Fatalf("exact-pin diagnostic=%+v error=%v", result, err)
			}
			t.Logf("current-source exact-pin diagnostic=%+v", result)
		})
	}
}

func TestNativeSyntheticRuntimeReviewActual160(t *testing.T) {
	binary, node := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if binary == "" || node == "" {
		t.Skip("exact160 and Node required; not gate evidence")
	}
	result, err := runNativePolicyCase(context.Background(), t.TempDir(), nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: "0.160.0"}, invocation: binary}, node, "review_allow_once", t.Logf)
	if err != nil || result.Outcome != "passed" || result.Reason != "synthetic_review_allowed_once" {
		t.Fatalf("synthetic Review=%+v err=%v", result, err)
	}
	t.Logf("synthetic Review diagnostic=%+v", result)
}

func TestNativeSyntheticRuntimeFullActual160(t *testing.T) {
	binary, node := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if binary == "" || node == "" {
		t.Skip("exact160 and Node required; not gate evidence")
	}
	for _, name := range []string{"profile_full_text", "profile_full_image", "profile_full_large", "profile_full_large_public", "profile_full_unknown", "profile_full_mutated", "profile_full_proof", "profile_full_credential", "profile_full_result_credential", "profile_full_result_structured"} {
		t.Run(name, func(t *testing.T) {
			result, err := runNativePolicyCase(context.Background(), t.TempDir(), nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: "0.160.0"}, invocation: binary}, node, name, t.Logf)
			if err != nil || result.Outcome != "passed" || result.Reason != "synthetic_full_diagnostic_only" {
				t.Fatalf("Full diagnostic=%+v err=%v", result, err)
			}
		})
	}
}

func TestNativeSyntheticRuntimeDenyBoundariesActual160(t *testing.T) {
	binary, node := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if binary == "" || node == "" {
		t.Skip("exact160 and Node required; not gate evidence")
	}
	for _, name := range []string{"review_reject", "review_deny_known", "review_sensitive_proof", "review_sensitive_credential"} {
		t.Run(name, func(t *testing.T) {
			result, err := runNativePolicyCase(context.Background(), t.TempDir(), nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: "0.160.0"}, invocation: binary}, node, name, t.Logf)
			if err != nil || result.Outcome != "passed" || result.NativeOutbound < 3 || result.NativeOutbound > 4 {
				t.Fatalf("Deny diagnostic=%+v %v", result, err)
			}
		})
	}
}
