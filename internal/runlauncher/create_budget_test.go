package runlauncher_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

// The defect this closes: every control call shared one 5s budget, including
// the create that makes the runtime verify a code signature (up to 30s) and
// then ask a person whether a client recognized by its publisher may carry the
// Root (up to 5 minutes, before this change). The inner bounds summed to
// sixty-six times the outer one, so a recognized launch could never finish:
// the launcher abandoned the request, the runtime went on to create a run
// nobody would collect, and the client did not start at all — where the design
// says a launch that cannot reach a person starts without a Root.
//
// The whole recognized tier of ADR-0016 was therefore unreachable in the
// shipped default configuration, and nothing failed to say so.
func TestADefaultLaunchOutlastsAnAskThatTakesLongerThanAControlCall(t *testing.T) {
	t.Parallel()

	// Longer than defaultControlTimeout (5s), shorter than the create budget.
	// A real ask is a person clicking; this stands in for one who is slow.
	const askDuration = 6 * time.Second

	arrived := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		time.Sleep(askDuration)
		// What is returned does not matter: the launch fails afterwards on a
		// grant it cannot validate either way. What this test observes is
		// whether the launcher was still there to receive an answer at all.
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: fixedDiscovery{session: localdiscovery.Session{
			BaseURL:           server.URL,
			ControlCredential: capability(0x71),
		}},
		BaseEnvironment: []string{"PATH=/usr/bin:/bin"},
		// Deliberately not set: this is about what a user's installation does,
		// and a test that configured the budget would prove nothing about it.
		Getwd: func() (string, error) {
			return t.TempDir(), nil
		},
		LookPath: func(string) (string, error) {
			return "/bin/echo", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	finished := make(chan error, 1)
	go func() {
		_, runErr := launcher.Run(context.Background(), []string{"echo"})
		finished <- runErr
	}()

	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the create request never reached the runtime")
	}
	select {
	case <-finished:
		if waited := time.Since(started); waited < askDuration {
			t.Fatalf(
				"the launcher gave up on the create after %v, before the "+
					"runtime had answered; a recognized client cannot be "+
					"approved inside a budget shorter than the ask",
				waited,
			)
		}
	case <-time.After(askDuration + 20*time.Second):
		t.Fatal("the create was not bounded at all")
	}
}
