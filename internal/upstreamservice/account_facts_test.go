package upstreamservice_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

func TestCodexQuotaFactsKeepMissingDistinctFromZeroAndExcludeUnknownFields(t *testing.T) {
	origin, _ := originidentity.ParseProviderOrigin("https://chatgpt.com")
	read, err := upstreamservice.ResolveRead(origin, upstreamservice.CodexRateLimits)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := read.Project([]byte(`{"plan_type":"pro","access_token":"never-project-this"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Limits) != 0 || missing.Credits != nil || missing.RateLimitResets != nil {
		t.Fatal("missing quotas were turned into known zero values")
	}
	available, err := read.Project([]byte(`{"plan_type":"pro","rate_limit_reset_credits":{"available_count":2,"applicable_available_count":1},"refresh_token":"never-project-this"}`))
	if err != nil || available.RateLimitResets == nil || available.RateLimitResets.AvailableCount != 2 ||
		available.RateLimitResets.ApplicableAvailableCount == nil || *available.RateLimitResets.ApplicableAvailableCount != 1 {
		t.Fatalf("banked reset count was lost: %+v, %v", available.RateLimitResets, err)
	}
	unknownApplicable, err := read.Project([]byte(`{"plan_type":"pro","rate_limit_reset_credits":{"available_count":0}}`))
	if err != nil || unknownApplicable.RateLimitResets == nil || unknownApplicable.RateLimitResets.AvailableCount != 0 ||
		unknownApplicable.RateLimitResets.ApplicableAvailableCount != nil {
		t.Fatalf("unknown applicability became zero: %+v, %v", unknownApplicable.RateLimitResets, err)
	}
	zero, err := read.Project([]byte(`{"plan_type":"pro","account_id":"workspace-B","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0,"limit_window_seconds":18000,"reset_after_seconds":3600,"reset_at":1800000000}},"credits":{"has_credits":true,"unlimited":false,"balance":"12.50"},"access_token":"never-project-this"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(zero.Limits) != 1 || zero.Limits[0].Primary == nil || zero.Limits[0].Primary.UsedPercent != 0 || zero.Limits[0].Secondary != nil || zero.Credits == nil || zero.Credits.Balance == nil || *zero.Credits.Balance != "12.50" {
		t.Fatal("quota window or exact credit balance lost its semantics")
	}
	wire, err := json.Marshal(zero)
	if err != nil || strings.Contains(string(wire), "never-project-this") {
		t.Fatal("private extension escaped the allowlisted projection")
	}
	for _, malformed := range []string{
		`{}`, `{"plan_type":null}`, `{"plan_type":"pro","rate_limit":{"allowed":true}}`,
		`{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0}}}`,
		`{"plan_type":"pro","credits":{"has_credits":true,"unlimited":false,"balance":12.5}}`,
		`{"plan_type":"pro","rate_limit_reset_credits":{}}`,
		`{"plan_type":"pro","rate_limit_reset_credits":{"available_count":-1}}`,
		`{"plan_type":"pro","rate_limit_reset_credits":{"available_count":1,"applicable_available_count":2}}`,
	} {
		if _, err := read.Project([]byte(malformed)); err == nil {
			t.Fatal("malformed quota payload accepted")
		}
	}
}

func TestOwnerResetCreditDetailsAreBoundedAndDoNotExposeExtraFields(t *testing.T) {
	base, _ := originidentity.ParseProviderOrigin("https://chatgpt.com")
	read, err := upstreamservice.ResolveRead(base, upstreamservice.CodexResetCreditDetails)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := read.Project([]byte(`{"available_count":2,"credits":[{"id":"credit-1","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-09-01T00:00:00Z","expires_at":"2026-10-01T00:00:00Z","title":"Full reset","profile_user_id":"private-user"},{"id":"credit-2","reset_type":"future","status":"unknown","granted_at":"2026-09-02T00:00:00Z"}],"access_token":"never-project-this"}`))
	if err != nil || facts.RateLimitResets == nil || facts.RateLimitResets.AvailableCount != 2 ||
		len(facts.RateLimitResets.Details) != 2 || facts.RateLimitResets.Details[0].ID != "credit-1" ||
		facts.RateLimitResets.Details[1].ResetType != "future" {
		t.Fatalf("reset detail projection = %+v, %v", facts.RateLimitResets, err)
	}
	wire, err := json.Marshal(facts)
	if err != nil || strings.Contains(string(wire), "private-user") || strings.Contains(string(wire), "never-project-this") {
		t.Fatal("private reset-credit extension escaped projection")
	}
	for _, body := range []string{
		`{"available_count":-1,"credits":[]}`,
		`{"available_count":1,"credits":[{"id":"same","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-09-01T00:00:00Z"},{"id":"same","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-09-01T00:00:00Z"}]}`,
		`{"available_count":1,"credits":[{"id":"credit-1","reset_type":"codex_rate_limits","status":"available","granted_at":"not-a-time"}]}`,
	} {
		if _, err := read.Project([]byte(body)); err == nil {
			t.Fatalf("invalid reset details accepted: %s", body)
		}
	}
}
