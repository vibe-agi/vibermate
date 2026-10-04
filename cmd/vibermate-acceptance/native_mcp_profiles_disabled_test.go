//go:build !vibermate_acceptance_synthetic

package main

import (
	"context"
	"testing"
)

func TestNativeUntaggedProfileRowsStayIncomplete(t *testing.T) {
	for _, name := range []string{"default_call_text", "default_call_image", "large_result_page", "review_allow_once", "sse_reconnect"} {
		result, err := runNativePolicyCase(context.Background(), t.TempDir(), nativePinnedClient{}, "", name)
		if err == nil || result.Outcome != "incomplete" || result.Reason != "synthetic_content_profiles_required" || result.NativeOutbound != 0 {
			t.Fatalf("untagged %s=%+v %v", name, result, err)
		}
	}
}
