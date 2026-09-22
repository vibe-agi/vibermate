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
	if len(missing.Limits) != 0 || missing.Credits != nil {
		t.Fatal("missing quotas were turned into known zero values")
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
	} {
		if _, err := read.Project([]byte(malformed)); err == nil {
			t.Fatal("malformed quota payload accepted")
		}
	}
}
