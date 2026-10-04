//go:build vibermate_acceptance_synthetic

package main

import (
	"context"
	"os"
	"testing"
)

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
	for _, name := range []string{"profile_full_text", "profile_full_image", "profile_full_large", "profile_full_unknown", "profile_full_mutated", "profile_full_proof", "profile_full_credential", "profile_full_result_credential", "profile_full_result_structured"} {
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
