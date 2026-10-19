//go:build !vibermate_native_secrets

package desktophost_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	_ "modernc.org/sqlite"
)

// Selected append faults live in runtimepersistence's actual-writer TEMP
// trigger test. This host case proves real child forwarding, settled tokens,
// frozen attribution, and restart retention without altering schema admission.
func TestTerminalObservationsPersistAcrossForwardingAndRestart(t *testing.T) {

	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"msg_usage","type":"message","role":"assistant","model":"claude-test",
		 "content":[{"type":"text","text":"managed reached"}],"stop_reason":"end_turn","stop_sequence":null,
		 "usage":{"input_tokens":4,"output_tokens":2}}`)
	}))
	defer provider.Close()
	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	dataDirectory := filepath.Join(root, "data")
	factory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(root, "private", "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog := fixedSelfTestCatalog(t)
	host, _ := startManagedHost(t, paths, dataDirectory, factory, catalog)
	defer func() { shutdownHost(t, host) }()
	endpoint := createManagedEndpoint(t, host.Runtime().UpstreamEndpoints(), provider.URL)
	account := createManagedAccount(t, host.Runtime().ProviderAccounts(), endpoint)
	environment := publishManagedEnvironment(t, host, provider.URL, endpoint, account)
	usage := host.Runtime().UsageRepository()
	policy, err := usage.UsagePolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = true
	if _, err := usage.SetUsagePolicy(context.Background(), policy, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// Wait for the actual optional writer before opening its synthetic
	// fixture database for exact retained-row assertions.
	readyDeadline := time.Now().Add(5 * time.Second)
	for host.Runtime().RawEvidenceStatistics().WriterID == "" && time.Now().Before(readyDeadline) {
		time.Sleep(time.Millisecond)
	}
	if host.Runtime().RawEvidenceStatistics().WriterID == "" {
		t.Fatal("optional owner not initialized")
	}
	databaseURL := url.URL{Scheme: "file", Path: filepath.Join(dataDirectory, "observations.db"), RawQuery: "mode=rw"}
	faults, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer faults.Close()
	runManagedChild(t, host, paths, environment, false)
	assertCalls := func(want int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			rows, err := faults.Query(`SELECT observation_json FROM runtime_usage_observations ORDER BY exchange_id LIMIT 20`)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			for rows.Next() {
				var encoded string
				if err = rows.Scan(&encoded); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				var observation runtimeusage.Observation
				if err = json.Unmarshal([]byte(encoded), &observation); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				if observation.Status != activity.StatusSucceeded || observation.AccountID != string(account) || observation.UpstreamModel != "claude-test" || !observation.Usage.InputUncached.Known || observation.Usage.InputUncached.Tokens != 4 || !observation.Usage.Output.Known || observation.Usage.Output.Tokens != 2 {
					rows.Close()
					t.Fatalf("terminal usage lost attribution/tokens: %+v", observation)
				}
				calls++
			}
			if err = errors.Join(rows.Err(), rows.Close()); err != nil {
				t.Fatal(err)
			}
			if calls == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("eventual optional usage=%d want=%d", calls, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	assertEvidence := func() {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			var subject, status, actualAccount, route string
			var revision, epoch int64
			err := faults.QueryRow(`SELECT subject_id,status,account_id,account_revision,credential_epoch,route_id FROM runtime_activities WHERE kind='exchange.completed' AND environment_id=? ORDER BY sequence DESC LIMIT 1`, environment.String()).Scan(&subject, &status, &actualAccount, &revision, &epoch, &route)
			if errors.Is(err, sql.ErrNoRows) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if status != "succeeded" || actualAccount != account.String() || revision != 1 || epoch != 1 || route != "route.anthropic" {
				t.Fatal("independent Activity lost frozen execution identity")
			}
			var outcome, origin string
			var bytesOut, bytesIn int64
			err = faults.QueryRow(`SELECT outcome,target_origin,bytes_out,bytes_in FROM runtime_egress_attempts WHERE parent_exchange_id=? AND purpose='provider_attempt'`, subject).Scan(&outcome, &origin, &bytesOut, &bytesIn)
			if err == nil && outcome == "completed" && origin != "https://api.anthropic.com" && bytesOut > 0 && bytesIn > 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("independent Egress terminal missing", err, outcome, origin, bytesOut, bytesIn)
			}
			time.Sleep(time.Millisecond)
		}
	}

	assertCalls(1)
	assertEvidence()

	runManagedChild(t, host, paths, environment, false)
	assertEvidence()
	assertCalls(2)
	shutdownHost(t, host)
	host, _ = startManagedHost(t, paths, dataDirectory, factory, catalog)
	assertCalls(2)
}
