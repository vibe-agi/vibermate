//go:build !vibermate_native_secrets

package desktophost_test

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	_ "modernc.org/sqlite"
)

func TestTerminalObservationFailuresKeepIndependentRecords(t *testing.T) {
	for _, test := range []struct {
		stage      string
		fault      string
		usageCalls int
	}{
		{"activity_terminal", `CREATE TRIGGER reject_recording BEFORE INSERT ON runtime_activities
WHEN NEW.kind='exchange.completed' BEGIN SELECT RAISE(ABORT,'synthetic activity failure'); END`, 1},
		{"activity_start", `CREATE TRIGGER reject_recording BEFORE INSERT ON runtime_activities
WHEN NEW.kind='exchange.started' BEGIN SELECT RAISE(ABORT,'synthetic activity failure'); END`, 1},
		{"usage", `CREATE TRIGGER reject_recording BEFORE INSERT ON runtime_usage_observations
BEGIN SELECT RAISE(ABORT,'synthetic usage failure'); END`, 0},
	} {
		t.Run(test.stage, func(t *testing.T) {
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
			// Inject a storage-boundary failure only for the selected recording. All
			// assertions below use public Runtime reads, never private SQLite contents.
			databaseURL := url.URL{Scheme: "file", Path: filepath.Join(dataDirectory, "runtime.db"), RawQuery: "mode=rw"}
			faults, err := sql.Open("sqlite", databaseURL.String())
			if err != nil {
				t.Fatal(err)
			}
			defer faults.Close()
			if _, err := faults.Exec(test.fault); err != nil {
				t.Fatal(err)
			}
			runManagedChild(t, paths, environment, false)
			deadline := time.Now().Add(3 * time.Second)
			for host.Runtime().Status().RecordingFailure == nil && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			status := host.Runtime().Status()
			if status.State != productruntime.RuntimeStateInitialized || status.Storage != productruntime.StorageStateHealthy ||
				status.RecordingFailure == nil || status.RecordingFailure.Operation != test.stage {
				t.Fatalf("recording failure lost its warning or stopped forwarding: %+v", status)
			}
			assertCalls := func(want int) {
				t.Helper()
				now := time.Now().UTC()
				period, err := runtimeusage.NewQuery(now.AddDate(0, 0, -1).Format("2006-01-02"), now.AddDate(0, 0, 1).Format("2006-01-02"), "UTC")
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				_, err = host.Runtime().UsageRepository().ScanUsage(context.Background(), runtimeusage.AggregationQuery{Period: period, Dimension: "account", Limit: 50}, "", now, func(bucket runtimeusage.UsageBucket) error {
					if bucket.Status != activity.StatusSucceeded || bucket.GroupID != string(account) ||
						bucket.Model != "claude-test" || !bucket.Usage.InputUncached.Known || bucket.Usage.InputUncached.Tokens != 4 ||
						!bucket.Usage.Output.Known || bucket.Usage.Output.Tokens != 2 {
						t.Errorf("terminal usage lost frozen attribution or declared tokens: %+v", bucket)
					}
					calls += bucket.Calls
					return nil
				})
				if err != nil || calls != want {
					t.Fatalf("recorded usage calls = %d, want %d: %v", calls, want, err)
				}
			}
			assertCalls(test.usageCalls)
			if test.stage != "activity_terminal" {
				assertManagedEvidence(t, host, environment, account)
			}
			if _, err := faults.Exec(`DROP TRIGGER reject_recording`); err != nil {
				t.Fatal(err)
			}
			runManagedChild(t, paths, environment, false)
			assertManagedEvidence(t, host, environment, account)
			assertCalls(test.usageCalls + 1)
			shutdownHost(t, host)
			host, _ = startManagedHost(t, paths, dataDirectory, factory, catalog)
			assertCalls(test.usageCalls + 1)
		})
	}
}
