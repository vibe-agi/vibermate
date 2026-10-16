package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientpath"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func doctorFixture(t *testing.T, extra http.HandlerFunc, controlDelay ...time.Duration) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	now := time.Now().UTC().Truncate(time.Millisecond)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x53}, 32))
	machine := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x36}, 32))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == servercontrol.RuntimeUserCurrentSessionPath {
			if len(controlDelay) > 0 {
				timer := time.NewTimer(controlDelay[0])
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
					return
				}
			}
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("control missing scoped credential")
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(servercontrol.RuntimeUserCurrentSession{Schema: servercontrol.RuntimeUserCurrentSessionSchema, InstanceID: "instance.test", APIVersion: "v1", ProductBuild: "test-build", SessionID: "login.test", MachineID: machine, User: servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"}})
			return
		}
		extra(w, r)
	}))
	t.Cleanup(server.Close)
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := clientpath.DefaultRemoteStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := workspaceidentity.Open(context.Background(), filepath.Join(directory, "identity"), bytes.NewReader(bytes.Repeat([]byte{0x36}, 64)), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	store, err := serverconnection.OpenLoginStore(filepath.Join(directory, "login"))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := serverconnection.NewLoginCredential(serverconnection.LoginCredentialInput{Target: target, InstanceID: "instance.test", UserID: "user.test", Username: "alice", SessionID: "login.test", SessionToken: token, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(credential); err != nil {
		t.Fatal(err)
	}
	return server.URL
}

func TestDoctorReportsExecutedChecksAndExplicitScope(t *testing.T) {
	var entries, catalogs atomic.Int32
	origin := doctorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodConnect:
			entries.Add(1)
			if r.Host != "vibermate-doctor.invalid:443" || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" {
				t.Error("probe authority or credentials invalid")
			}
			w.WriteHeader(407)
			io.WriteString(w, `{"reasonCode":"proxy_authentication_required"}`)
		case r.URL.Path == capturecontrol.EnvironmentsPath:
			catalogs.Add(1)
			io.WriteString(w, `{"schema":"vibermate-capture-environments/v1","items":[{"id":"work","name":"Work","clientToolAccessEnabled":false}]}`)
		default:
			t.Errorf("unexpected doctor action: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	for _, selected := range []string{"", "work", "missing"} {
		args := []string{"doctor", "--server", origin}
		if selected != "" {
			args = append(args, "--env", selected)
		}
		var out strings.Builder
		code, key := execute(args, []string{"LANG=en_US.UTF-8"}, strings.NewReader(""), &out, io.Discard)
		want := 0
		if selected == "missing" {
			want = 1
		}
		if code != want || key != "" {
			t.Fatalf("env=%q code=%d key=%s output=%s", selected, code, key, &out)
		}
		for _, expected := range []string{"Selected Runtime", "application-direct", "PASS Control/session/machine", "PASS Proxy entry", "407", "NOT CHECKED Capture creation", "NOT CHECKED Authorized proxy forwarding", "NOT CHECKED Native client login/startup", "NOT CHECKED Upstream/model inference"} {
			if !strings.Contains(out.String(), expected) {
				t.Fatalf("lacks %q: %s", expected, &out)
			}
		}
		envText := "NOT CHECKED Environment"
		if selected == "work" {
			envText = "PASS Environment"
		}
		if selected == "missing" {
			envText = "FAIL Environment"
		}
		if !strings.Contains(out.String(), envText) {
			t.Fatalf("missing environment outcome: %s", &out)
		}
	}
	if entries.Load() != 3 || catalogs.Load() != 2 {
		t.Fatalf("checks executed: entries=%d catalogs=%d", entries.Load(), catalogs.Load())
	}
}

func TestDoctorGrammarKeepsStatusUnchanged(t *testing.T) {
	for _, args := range [][]string{{"doctor", "--env", "work"}, {"doctor", "--env", "work", "--server", "localhost:9666"}, {"doctor", "--server", "localhost:9666", "--env", "work"}} {
		if _, err := parseStatus(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"doctor", "--env"}, {"doctor", "--env", "bad ID"}, {"doctor", "--env", ""}, {"doctor", "--env", "work", "--env", "work"}, {"doctor", "--server", "localhost:9666", "--server", "localhost:9666"}, {"doctor", "--json"}, {"doctor", "extra"}, {"status", "--env", "work"}} {
		if _, err := parseStatus(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestDoctorCancellationReturns130AndJoinsProbe(t *testing.T) {
	entered := make(chan struct{})
	origin := doctorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Error("unexpected action")
			w.WriteHeader(404)
			return
		}
		close(entered)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		code, _ := executeContext(ctx, []string{"doctor", "--server", origin}, []string{"LANG=en_US.UTF-8"}, strings.NewReader(""), io.Discard, io.Discard)
		done <- code
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not start")
	}
	cancel()
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("code=%d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("doctor did not cancel")
	}
}

func TestDoctorMissingLoginDoesNotClaimTransportOrEntryPASS(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, locale := range []string{"en_US.UTF-8", "zh_CN.UTF-8"} {
		var out strings.Builder
		code, key := execute([]string{"doctor", "--server", "127.0.0.1:1"}, []string{"LANG=" + locale}, strings.NewReader(""), &out, io.Discard)
		if code != 1 || key != "" || strings.Contains(out.String(), "PASS") || !strings.Contains(out.String(), "login_or_machine") || !strings.Contains(out.String(), "vibermate login --server") {
			t.Fatalf("code=%d key=%s out=%s", code, key, &out)
		}
	}
}

func TestDoctorControlAndEntryAllowOperationsBeyondFiveSeconds(t *testing.T) {
	for _, stage := range []string{"control", "entry"} {
		t.Run(stage, func(t *testing.T) {
			delay := time.Duration(0)
			if stage == "control" {
				delay = 5100 * time.Millisecond
			}
			origin := doctorFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodConnect {
					t.Error("unexpected diagnostic action")
					w.WriteHeader(404)
					return
				}
				if stage == "entry" {
					timer := time.NewTimer(5100 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-r.Context().Done():
						return
					}
				}
				w.WriteHeader(407)
				io.WriteString(w, `{"reasonCode":"proxy_authentication_required"}`)
			}, delay)
			var out strings.Builder
			code, key := execute([]string{"doctor", "--server", origin}, []string{"LANG=en_US.UTF-8"}, strings.NewReader(""), &out, io.Discard)
			if code != 0 || key != "" || !strings.Contains(out.String(), "PASS Proxy entry") {
				t.Fatalf("%s code=%d key=%s output=%s", stage, code, key, &out)
			}
		})
	}
}

func TestDoctorNeverPrintsUntrustedResponseContent(t *testing.T) {
	const secret = "doctor-secret-Bearer-not-for-output"
	origin := doctorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			w.Header().Set("Proxy-Authenticate", secret)
			w.WriteHeader(407)
			io.WriteString(w, `{"reasonCode":"`+secret+`","detailCode":"`+secret+`"}`)
			return
		}
		w.WriteHeader(403)
		io.WriteString(w, `{"reasonCode":"`+secret+`","title":"`+secret+`"}`)
	})
	for _, locale := range []string{"en_US.UTF-8", "zh_CN.UTF-8"} {
		var out strings.Builder
		code, key := execute([]string{"doctor", "--server", origin, "--env", "work"}, []string{"LANG=" + locale}, strings.NewReader(""), &out, io.Discard)
		if code != 1 || key != "" || strings.Contains(out.String(), secret) || !strings.Contains(out.String(), "connect_ingress") || !strings.Contains(out.String(), "permission") {
			t.Fatalf("code=%d key=%s out=%s", code, key, &out)
		}
	}
}

func TestLateRemoteCatalogRejectionRequestsLogin(t *testing.T) {
	origin := doctorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			w.WriteHeader(407)
			io.WriteString(w, `{"reasonCode":"proxy_authentication_required"}`)
			return
		}
		if r.URL.Path != capturecontrol.EnvironmentsPath {
			t.Errorf("unexpected action %s", r.URL.Path)
		}
		w.WriteHeader(401)
		io.WriteString(w, `{"type":"urn:vibermate:error:control-principal-unauthorized","title":"SECRET","status":401,"code":"control_principal_unauthorized"}`)
	})
	var out strings.Builder
	code, key := execute([]string{"profiles", "--server", origin}, nil, strings.NewReader(""), &out, io.Discard)
	if code != 1 || key != keyRemoteLoginRequired {
		t.Fatalf("profiles code=%d key=%s", code, key)
	}
	target, _ := serverconnection.ParseTarget(origin)
	directory, _ := clientpath.DefaultRemoteStateDirectory()
	launcher, err := runlauncher.New(runlauncher.Config{Remote: &runlauncher.RemoteConfig{Target: target, StateDirectory: directory, DisplayName: "fixture", Clock: commandClock{}, Random: strings.NewReader(strings.Repeat("x", 4096))}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = launcher.Environments(context.Background())
	var failure *runlauncher.ControlFailure
	if !errors.Is(err, runlauncher.ErrRemoteLoginRequired) || !errors.As(err, &failure) || failure.Status != 401 || failure.ReasonCode != "control_principal_unauthorized" {
		t.Fatalf("catalog lost login classification or metadata: %v", err)
	}
	out.Reset()
	code, key = execute([]string{"doctor", "--server", origin, "--env", "work"}, []string{"LANG=en_US.UTF-8"}, strings.NewReader(""), &out, io.Discard)
	if code != 1 || key != "" || !strings.Contains(out.String(), "login_or_machine") {
		t.Fatalf("doctor code=%d key=%s out=%s", code, key, &out)
	}
}
