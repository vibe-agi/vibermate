package desktopcontrol_test

import (
	"bytes"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestDesktopSessionRotationSurvivesLostResponseAndRetiresOldScope(
	t *testing.T,
) {
	fixture := newSessionFixture(t, 2*time.Second, 20*time.Second, 5*time.Second)

	stateResponse := doRequest(
		t,
		fixture.router,
		fixture.authority,
		http.MethodGet,
		desktopcontrol.SessionStatePath,
		fixture.readToken,
		nil,
	)
	if stateResponse.Code != http.StatusOK ||
		stateResponse.Header().Get("Cache-Control") != "no-store" ||
		bytes.Contains(stateResponse.Body.Bytes(), []byte(fixture.readToken)) ||
		bytes.Contains(stateResponse.Body.Bytes(), []byte(fixture.writeToken)) {
		t.Fatalf(
			"initial session state code=%d body=%s",
			stateResponse.Code,
			stateResponse.Body.Bytes(),
		)
	}
	var initial desktopcontrol.SessionState
	decodeResponse(t, stateResponse, &initial)
	if initial.Schema != desktopcontrol.SessionStateSchema || initial.Revision != 1 {
		t.Fatalf("initial session state = %+v", initial)
	}
	stateWithQuery := doRequest(
		t,
		fixture.router,
		fixture.authority,
		http.MethodGet,
		desktopcontrol.SessionStatePath+"?unexpected=1",
		fixture.readToken,
		nil,
	)
	if stateWithQuery.Code != http.StatusUnprocessableEntity {
		t.Fatalf("session state with query code=%d", stateWithQuery.Code)
	}
	malformed := renewSession(
		fixture.router,
		fixture.authority,
		fixture.writeToken,
		1,
		"",
	)
	if malformed.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed renewal code=%d", malformed.Code)
	}

	fixture.clock.advance(time.Second)
	const firstKey = "session-renewal-0001"
	first := renewSession(
		fixture.router,
		fixture.authority,
		fixture.writeToken,
		1,
		firstKey,
	)
	if first.Code != http.StatusOK ||
		first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf(
			"first renewal code=%d body=%s",
			first.Code,
			first.Body.Bytes(),
		)
	}
	firstBody := bytes.Clone(first.Body.Bytes())
	var rotated desktopcontrol.SessionRotation
	decodeResponse(t, first, &rotated)
	if rotated.Schema != desktopcontrol.SessionRotationSchema ||
		rotated.Revision != 2 ||
		rotated.ReadToken == fixture.readToken ||
		rotated.WriteToken == fixture.writeToken ||
		rotated.ReadToken == rotated.WriteToken ||
		!rotated.ExpiresAt.After(initial.ExpiresAt) {
		t.Fatalf("rotated session = %+v", rotated)
	}

	// The original response is treated as lost. Its retired write token may
	// cross the original expiry only to replay this exact command.
	fixture.clock.advance(2 * time.Second)
	replay := renewSession(
		fixture.router,
		fixture.authority,
		fixture.writeToken,
		1,
		firstKey,
	)
	if replay.Code != http.StatusOK || !bytes.Equal(replay.Body.Bytes(), firstBody) {
		t.Fatalf("lost-response replay code=%d body=%s", replay.Code, replay.Body.Bytes())
	}
	differentKey := renewSession(
		fixture.router,
		fixture.authority,
		fixture.writeToken,
		1,
		"session-renewal-other",
	)
	if differentKey.Code != http.StatusConflict {
		t.Fatalf("retired token with different key code=%d", differentKey.Code)
	}
	differentRevision := renewSession(
		fixture.router,
		fixture.authority,
		fixture.writeToken,
		2,
		firstKey,
	)
	if differentRevision.Code != http.StatusConflict {
		t.Fatalf("idempotency key with different revision code=%d", differentRevision.Code)
	}
	staleCurrent := renewSession(
		fixture.router,
		fixture.authority,
		rotated.WriteToken,
		1,
		"session-renewal-stale",
	)
	if staleCurrent.Code != http.StatusConflict {
		t.Fatalf("current token with stale revision code=%d", staleCurrent.Code)
	}

	oldRead := doRequest(
		t,
		fixture.router,
		fixture.authority,
		http.MethodGet,
		"/api/v1/status",
		fixture.readToken,
		nil,
	)
	if oldRead.Code != http.StatusUnauthorized ||
		bytes.Contains(oldRead.Body.Bytes(), []byte(fixture.readToken)) {
		t.Fatalf("retired read token code=%d body=%s", oldRead.Code, oldRead.Body.Bytes())
	}
	oldWrite := doMutation(
		t,
		fixture.router,
		fixture.authority,
		"/api/v1/offline-hold/actions/enter",
		fixture.writeToken,
		0,
		"retired-write-0001",
		nil,
	)
	if oldWrite.Code != http.StatusUnauthorized {
		t.Fatalf("retired write token retained ordinary scope: %d", oldWrite.Code)
	}
	wrongScope := renewSession(
		fixture.router,
		fixture.authority,
		rotated.ReadToken,
		2,
		"session-renewal-read-token",
	)
	if wrongScope.Code != http.StatusUnauthorized {
		t.Fatalf("read token renewed a session: %d", wrongScope.Code)
	}
	newRead := doRequest(
		t,
		fixture.router,
		fixture.authority,
		http.MethodGet,
		"/api/v1/status",
		rotated.ReadToken,
		nil,
	)
	if newRead.Code != http.StatusOK {
		t.Fatalf("rotated read token code=%d body=%s", newRead.Code, newRead.Body.Bytes())
	}
	writeCannotReadState := doRequest(
		t,
		fixture.router,
		fixture.authority,
		http.MethodGet,
		desktopcontrol.SessionStatePath,
		rotated.WriteToken,
		nil,
	)
	if writeCannotReadState.Code != http.StatusUnauthorized {
		t.Fatalf("write token read session metadata: %d", writeCannotReadState.Code)
	}

	fixture.clock.advance(4 * time.Second)
	expiredReplay := renewSession(
		fixture.router,
		fixture.authority,
		fixture.writeToken,
		1,
		firstKey,
	)
	if expiredReplay.Code != http.StatusUnauthorized {
		t.Fatalf("expired replay window code=%d", expiredReplay.Code)
	}
	second := renewSession(
		fixture.router,
		fixture.authority,
		rotated.WriteToken,
		2,
		"session-renewal-0002",
	)
	if second.Code != http.StatusOK {
		t.Fatalf("second generation renewal code=%d body=%s", second.Code, second.Body.Bytes())
	}
	var twiceRotated desktopcontrol.SessionRotation
	decodeResponse(t, second, &twiceRotated)
	if twiceRotated.Revision != 3 {
		t.Fatalf("second rotation = %+v", twiceRotated)
	}
}

func TestDesktopSessionRotationSerializesConcurrentCommands(t *testing.T) {
	t.Run("same key replays one committed generation", func(t *testing.T) {
		fixture := newSessionFixture(t, time.Minute, time.Hour, time.Minute)
		const workers = 24
		start := make(chan struct{})
		results := make(chan *httptest.ResponseRecorder, workers)
		var wait sync.WaitGroup
		for range workers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				results <- renewSession(
					fixture.router,
					fixture.authority,
					fixture.writeToken,
					1,
					"concurrent-renew-0001",
				)
			}()
		}
		close(start)
		wait.Wait()
		close(results)
		var canonical []byte
		for result := range results {
			if result.Code != http.StatusOK {
				t.Fatalf("same-key concurrent status=%d body=%s", result.Code, result.Body.Bytes())
			}
			if canonical == nil {
				canonical = bytes.Clone(result.Body.Bytes())
			} else if !bytes.Equal(canonical, result.Body.Bytes()) {
				t.Fatal("same idempotency key returned different token generations")
			}
		}
	})

	t.Run("different keys commit exactly once", func(t *testing.T) {
		fixture := newSessionFixture(t, time.Minute, time.Hour, time.Minute)
		const workers = 16
		start := make(chan struct{})
		statuses := make(chan int, workers)
		var wait sync.WaitGroup
		for index := range workers {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				<-start
				result := renewSession(
					fixture.router,
					fixture.authority,
					fixture.writeToken,
					1,
					"concurrent-unique-"+strconv.FormatInt(int64(1000+index), 10),
				)
				statuses <- result.Code
			}(index)
		}
		close(start)
		wait.Wait()
		close(statuses)
		committed := 0
		conflicted := 0
		for status := range statuses {
			switch status {
			case http.StatusOK:
				committed++
			case http.StatusConflict:
				conflicted++
			default:
				t.Fatalf("different-key concurrent status=%d", status)
			}
		}
		if committed != 1 || conflicted != workers-1 {
			t.Fatalf("committed=%d conflicted=%d", committed, conflicted)
		}
	})
}

type sessionFixture struct {
	router     http.Handler
	authority  string
	readToken  string
	writeToken string
	clock      *sessionClock
}

func newSessionFixture(
	t *testing.T,
	initialTTL time.Duration,
	rotationTTL time.Duration,
	replayTTL time.Duration,
) sessionFixture {
	t.Helper()
	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	clock := &sessionClock{
		now: time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC),
	}
	readToken := capability(0x71)
	writeToken := capability(0x72)
	authenticator, err := desktopcontrol.NewAuthenticator(
		desktopcontrol.CapabilityGrant{
			ReadToken:  readToken,
			WriteToken: writeToken,
			ExpiresAt:  clock.Now().Add(initialTTL),
			Revision:   1,
			Rotation: &desktopcontrol.SessionRotationPolicy{
				Lifetime:  rotationTTL,
				ReplayTTL: replayTTL,
				Random:    rand.Reader,
			},
		},
		clock,
	)
	if err != nil {
		t.Fatal(err)
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness:       readyState(true),
		Status:          runtime,
		Environments:    runtime.Environments(),
		Assignments:     runtime.CaptureAssignments(),
		Clock:           desktopcontrol.SystemClock{},
		Activities:      runtime.Activities(),
		Connections:     runtime.ConnectionEvents(),
		Egress:          runtime.EgressAttempts(),
		Approvals:       runtime.ToolApprovals(),
		Accounts:        runtime.ProviderAccounts(),
		Offline:         runtime,
		ConnectionRules: runtime.ConnectionRules(),
		CaptureRuns:     runtime.CaptureRunReader(),
		ManualCaptures:  runtime.ManualCaptures(),
	})
	if err != nil {
		t.Fatal(err)
	}
	const authority = "127.0.0.1:43138"
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{
		Authority:      authority,
		AllowedOrigins: []string{"tauri://localhost"},
		Authenticator:  authenticator,
		Application:    application,
		Bootstrap:      emptyBootstrap(),
		CLIControl: http.HandlerFunc(func(
			http.ResponseWriter,
			*http.Request,
		) {
		}),
		ManualCaptures:   rejectingManualCaptureHandler{},
		DesktopPrincipal: desktopManualPrincipal(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	return sessionFixture{
		router:     router,
		authority:  authority,
		readToken:  readToken,
		writeToken: writeToken,
		clock:      clock,
	}
}

func renewSession(
	handler http.Handler,
	authority string,
	token string,
	revision uint64,
	key string,
) *httptest.ResponseRecorder {
	request := newRequest(
		http.MethodPost,
		authority,
		desktopcontrol.SessionRenewalPath,
		token,
		nil,
	)
	request.Header.Set("If-Match", strconv.FormatUint(revision, 10))
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

type sessionClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *sessionClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *sessionClock) advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}
