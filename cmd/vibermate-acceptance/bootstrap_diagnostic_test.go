package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
)

// Replacing or retaining HOME/CFFIXED under login policy must fail this test.
func TestDiagnosticDaemonHomeLoginPreservesOnlyLoginPolicy(t *testing.T) {
	base := []string{"HOME=/Users/disposable", "PATH=/usr/bin", "CFFIXED_USER_HOME=/private/fixture", "TOKEN=sentinel"}
	got, err := diagnosticDaemonEnvironment(base, filepath.Join(t.TempDir(), "data"), daemonHomeLogin)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"HOME=/Users/disposable", "PATH=/usr/bin", "TOKEN=sentinel"}
	if !slices.Equal(got, want) {
		t.Fatal("environment policy mismatch")
	}
	if len(base) != 4 || base[2] != "CFFIXED_USER_HOME=/private/fixture" {
		t.Fatal("mutated parent environment")
	}
}

// Choosing an isolated HOME for normal acceptance must fail this test.
func TestDiagnosticDaemonHomeNormalPreservesParent(t *testing.T) {
	for _, configured := range []config{defaultConfig(), {}} {
		base := []string{"HOME=/Users/disposable", "PATH=/usr/bin", "CFFIXED_USER_HOME=/private/app", "TOKEN=sentinel"}
		before := slices.Clone(base)
		directory := filepath.Join(t.TempDir(), "data")
		got, err := diagnosticDaemonEnvironment(base, directory, daemonHomePolicy(configured.diagnosticDaemonHome))
		if err != nil || !slices.Equal(got, []string{"HOME=/Users/disposable", "PATH=/usr/bin", "TOKEN=sentinel"}) {
			t.Error("normal environment did not preserve parent HOME and remove only CFFIXED")
		}
		if !slices.Equal(base, before) {
			t.Error("normal environment mutated parent")
		}
		if _, err := os.Stat(filepath.Join(directory, "acceptance-home")); !os.IsNotExist(err) {
			t.Error("normal environment created replacement HOME")
		}
		if initialBootstrapDiagnostic(daemonHomePolicy(configured.diagnosticDaemonHome)).Policy != daemonHomeLogin {
			t.Error("normal metadata does not describe login policy")
		}
		if len(got) > 0 {
			got[0] = "HOME=/Users/changed"
			if !slices.Equal(base, before) {
				t.Error("normal environment aliases parent storage")
			}
		}
	}
}

func TestBootstrapDiagnosticRejectsReportDestinationCollision(t *testing.T) {
	for _, alias := range []string{"identical", "parent symlink", "missing parent symlink", "case basename", "unicode basename", "hardlink"} {
		t.Run(alias, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			diagnostic := filepath.Join(directory, "observation.json")
			report := diagnostic
			switch alias {
			case "parent symlink", "missing parent symlink":
				link := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(directory, link); err != nil {
					t.Skip("symlink unavailable")
				}
				report = filepath.Join(link, "observation.json")
				if alias == "missing parent symlink" {
					diagnostic = filepath.Join(directory, "future", "observation.json")
					report = filepath.Join(link, "future", "observation.json")
				}
			case "case basename":
				report = filepath.Join(directory, "Observation.json")
			case "unicode basename":
				diagnostic = filepath.Join(directory, "caf\u00e9.json")
				report = filepath.Join(directory, "cafe\u0301.json")
			case "hardlink":
				if err := os.WriteFile(diagnostic, []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
				report = filepath.Join(directory, "linked.json")
				if err := os.Link(diagnostic, report); err != nil {
					t.Skip("hardlink unavailable")
				}
			}
			configured := config{bootstrapDiagnosticPath: diagnostic, reportPath: report, deterministicOnly: true}
			if err := validateBootstrapDiagnosticConfig(configured); err == nil || !strings.Contains(err.Error(), "destinations collide") {
				t.Fatal("report/diagnostic destination collision accepted")
			}
			if _, err := parseConfig([]string{"--deterministic-only", "--bootstrap-diagnostic=" + diagnostic, "--report=" + report}); err == nil || !strings.Contains(err.Error(), "destinations collide") {
				t.Fatal("output collision was not rejected before App inspection")
			}
			if alias == "hardlink" {
				raw, err := os.ReadFile(diagnostic)
				if err != nil || string(raw) != "preserve" {
					t.Fatal("validation modified output")
				}
			}
		})
	}
}

func TestBootstrapDiagnosticDistinctOutputsRemainSeparate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private writer ownership unsupported on Windows")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	diagnostic := filepath.Join(directory, "observation.json")
	reportPath := filepath.Join(directory, "v7.json")
	configured := config{bootstrapDiagnosticPath: diagnostic, reportPath: reportPath, deterministicOnly: true}
	if err := validateBootstrapDiagnosticConfig(configured); err != nil {
		t.Fatal("distinct output destinations rejected")
	}
	recorder := &bootstrapDiagnosticRecorder{value: initialBootstrapDiagnostic(daemonHomeIsolated)}
	if err := writeBootstrapDiagnostic(diagnostic, recorder); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeReport(reportPath, newReport(time.Unix(1, 0), acceptanceClient{ID: acceptanceClientClaudeCode, Version: "2.1.220"})); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(diagnostic)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("V7 report overwrote diagnostic")
	}
	configured.reportPath = diagnostic
	if err := validateBootstrapDiagnosticConfig(configured); err == nil {
		t.Fatal("existing diagnostic overwrite was accepted")
	}
	final, err := os.ReadFile(diagnostic)
	if err != nil || !bytes.Equal(before, final) {
		t.Fatal("collision validation modified diagnostic")
	}
}

type diagnosticCloseObserver struct {
	io.Closer
	closed chan struct{}
}

func (reader diagnosticCloseObserver) Close() error {
	err := reader.Closer.Close()
	close(reader.closed)
	return err
}

func TestBootstrapDiagnosticTerminalWaitsForPendingDecoderCallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private writer ownership unsupported on Windows")
	}
	for _, frame := range []int{1, 2} {
		t.Run(string(rune('0'+frame)), func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "observation.json")
			recorder := &bootstrapDiagnosticRecorder{}
			if !recorder.claim(daemonHomeIsolated) {
				t.Fatal("bootstrap claim failed")
			}
			reader, writer := io.Pipe()
			defer writer.Close()
			defer reader.Close()
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			decoderDone := make(chan struct{})
			frames := diagnosticTestFrames(t)
			go func() {
				defer close(decoderDone)
				_, _ = decodeDescriptorObserved(reader, func(got int) {
					if got == frame {
						close(entered)
						<-release
					}
					recorder.observe(got)
				})
			}()
			go func() { _, _ = writer.Write(frames) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("decoder did not reach callback barrier")
			}
			readerClosed := make(chan struct{})
			serialized := make(chan error, 1)
			go func() {
				closeBootstrapDecoder(diagnosticCloseObserver{Closer: reader, closed: readerClosed}, decoderDone)
				recorder.finish(bootstrapDiagnostic{Started: true, Outcome: "context_done"}, time.Now(), newBoundedBuffer(64<<10))
				serialized <- writeBootstrapDiagnostic(path, recorder)
			}()
			select {
			case <-readerClosed:
			case <-time.After(time.Second):
				t.Fatal("cleanup did not close decoder reader")
			}
			// The decoder is positively parked in an observed callback, and cleanup
			// positively closed its reader. Serialization must remain blocked until
			// that callback is released; the timeout only bounds this negative check.
			select {
			case err := <-serialized:
				unblock()
				<-decoderDone
				if err != nil {
					t.Fatal(err)
				}
				t.Fatal("terminal observation serialized before pending decoder callback")
			case <-time.After(100 * time.Millisecond):
			}
			unblock()
			select {
			case err := <-serialized:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("terminal observation did not finish after decoder callback")
			}
			select {
			case <-decoderDone:
			default:
				t.Fatal("decoder was not joined before serialization")
			}
			var got bootstrapDiagnostic
			raw, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(raw, &got) != nil {
				t.Fatal("terminal observation missing")
			}
			if !got.ProgressValidated || (frame == 2 && !got.SecondFrameComplete) || got.Outcome != "context_done" {
				t.Fatal("settled callback or selected outcome lost")
			}
			if got != recorder.snapshot() {
				t.Fatal("decoder updated observation after serialization")
			}
		})
	}
}

func diagnosticTestFrames(t *testing.T) []byte {
	t.Helper()
	progress, err := json.Marshal(desktopbootstrap.RuntimeStartingProgress())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := json.Marshal(desktopbootstrap.Descriptor{
		Schema: desktopbootstrap.DescriptorSchema, InstanceID: "synthetic", ProcessID: 1,
		BaseURL: "http://127.0.0.1:41000", APIVersions: []string{"v1"}, EventVersions: []string{},
		BootstrapNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("SENTINEL_SECRET_NONCE"), 2)[:capabilityBytes]),
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(append(progress, '\n'), append(descriptor, '\n')...)
}

func TestBootstrapDiagnosticObservesOnlyValidatedProgressAndCompleteSecondFrame(t *testing.T) {
	valid := diagnosticTestFrames(t)
	firstEnd := bytes.IndexByte(valid, '\n') + 1
	for _, test := range []struct {
		name    string
		frames  []byte
		want    []int
		success bool
	}{
		{"valid", valid, []int{1, 2}, true},
		{"invalid progress", []byte("{}\n{}\n"), nil, false},
		{"invalid descriptor", append(bytes.Clone(valid[:firstEnd]), []byte("{}\n")...), []int{1, 2}, false},
		{"partial descriptor", append(bytes.Clone(valid[:firstEnd]), []byte("{}")...), []int{1}, false},
		{"oversize second frame", append(bytes.Clone(valid[:firstEnd]), []byte(strings.Repeat("x", bootstrapLimit-firstEnd)+"\n")...), []int{1, 2}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got []int
			_, err := decodeDescriptorObserved(bytes.NewReader(test.frames), func(frame int) { got = append(got, frame) })
			if !slices.Equal(got, test.want) {
				t.Fatal("validated progress or complete frame callback not observed")
			}
			if (err == nil) != test.success {
				t.Fatal("decoder validation changed")
			}
		})
	}
}

func TestBootstrapDiagnosticPrivateCapabilityFreeObservation(t *testing.T) {
	if runtime.GOOS == "windows" {
		if err := requirePrivateAuditOwnership(nil, true); err == nil || !strings.Contains(err.Error(), "unsupported on Windows") {
			t.Fatal("unexpected Windows ownership result")
		}
		return
	}
	recorder := &bootstrapDiagnosticRecorder{}
	if !recorder.claim(daemonHomeLogin) {
		t.Fatal("first bootstrap was not claimed")
	}
	if _, err := decodeDescriptorObserved(bytes.NewReader(diagnosticTestFrames(t)), recorder.observe); err != nil {
		t.Fatal(err)
	}
	stderr := newBoundedBuffer(64 << 10)
	_, _ = stderr.Write(bytes.Repeat([]byte("SENTINEL_SECRET_STDERR"), 4000))
	parent := []string{"HOME=/Users/disposable", "CFFIXED_USER_HOME=/private/fixture"}
	child, err := diagnosticDaemonEnvironment(parent, filepath.Join(t.TempDir(), "data"), daemonHomeLogin)
	if err != nil {
		t.Fatal(err)
	}
	parentHome, parentHasHome := daemonEnvironmentFact(parent, "HOME")
	childHome, childHasHome := daemonEnvironmentFact(child, "HOME")
	_, parentCFFixed := daemonEnvironmentFact(parent, "CFFIXED_USER_HOME")
	_, childCFFixed := daemonEnvironmentFact(child, "CFFIXED_USER_HOME")
	recorder.finish(bootstrapDiagnostic{Started: true, HomeMatchesParent: parentHasHome && childHasHome && parentHome == childHome, ParentCFFixedPresent: parentCFFixed, ChildCFFixedPresent: childCFFixed, Outcome: "ready"}, time.Now().Add(-time.Second), stderr)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "observation.json")
	if err := writeBootstrapDiagnostic(path, recorder); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("diagnostic file was not produced")
	}
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("SENTINEL_SECRET_NONCE"), 2)[:capabilityBytes])
	if len(raw) > 4096 || bytes.Contains(raw, []byte("SENTINEL_SECRET")) || bytes.Contains(raw, []byte(nonce)) || bytes.Contains(raw, []byte("/Users/disposable")) {
		t.Fatal("private diagnostic contract violated")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("diagnostic permissions")
	}
	var got bootstrapDiagnostic
	if json.Unmarshal(raw, &got) != nil || got.Schema != "vibermate.bootstrap-home-diagnostic/v1" || got.Policy != daemonHomeLogin || got.Outcome != "ready" || !got.ProgressValidated || !got.SecondFrameComplete || !got.Started || !got.HomeMatchesParent || !got.ParentCFFixedPresent || got.ChildCFFixedPresent || got.StderrBytes != 64<<10 || !got.StderrTruncated || got.ElapsedMillis < 0 {
		t.Fatal("diagnostic observation mismatch")
	}
}

func TestBootstrapDiagnosticNotStartedBeforeDaemonInvocation(t *testing.T) {
	recorder := &bootstrapDiagnosticRecorder{value: initialBootstrapDiagnostic("")}
	configured := config{bootstrapDiagnostic: recorder}
	if _, err := startDaemon(nil, configured, "", ""); err == nil {
		t.Fatal("nil startup context accepted")
	}
	first := recorder.snapshot()
	if first.Outcome != "not_started" || first.Started || first.Policy != daemonHomeLogin {
		t.Fatal("early startup diagnostic mismatch")
	}
	configured.diagnosticDaemonHome = "login"
	_, _ = startDaemon(nil, configured, "", "")
	if recorder.snapshot() != first {
		t.Fatal("later invocation overwrote first observation")
	}
}

func TestBootstrapDiagnosticWriterRejectsUnsafeTargets(t *testing.T) {
	for _, target := range []string{"existing", "symlink", "parent symlink", "unsafe parent", "parent file", "missing parent", "relative", "unclean"} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(dir, "original")
			if err := os.WriteFile(original, []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "observation.json")
			switch target {
			case "existing":
				path = original
			case "symlink":
				if err := os.Symlink(original, path); err != nil {
					t.Skip("symlink unavailable")
				}
			case "parent symlink":
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(dir, link); err != nil {
					t.Skip("symlink unavailable")
				}
				path = filepath.Join(link, "observation.json")
			case "unsafe parent":
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			case "parent file":
				path = filepath.Join(original, "observation.json")
			case "missing parent":
				path = filepath.Join(dir, "missing", "observation.json")
			case "relative":
				path = "observation.json"
			case "unclean":
				path = dir + "/../observation.json"
			}
			recorder := &bootstrapDiagnosticRecorder{value: bootstrapDiagnostic{Schema: "vibermate.bootstrap-home-diagnostic/v1", Policy: daemonHomeIsolated, Outcome: "not_started"}}
			if err := writeBootstrapDiagnostic(path, recorder); err == nil {
				t.Fatal("unsafe diagnostic target accepted")
			}
			raw, err := os.ReadFile(original)
			if err != nil || string(raw) != "preserve" {
				t.Fatal("existing target modified")
			}
		})
	}
}

func TestBootstrapDiagnosticRejectsUnboundedVocabulary(t *testing.T) {
	for _, field := range []string{"schema", "policy", "outcome"} {
		value := bootstrapDiagnostic{Schema: "vibermate.bootstrap-home-diagnostic/v1", Policy: daemonHomeIsolated, Outcome: "not_started"}
		switch field {
		case "schema":
			value.Schema = "SENTINEL_SECRET"
		case "policy":
			value.Policy = "SENTINEL_SECRET"
		case "outcome":
			value.Outcome = "SENTINEL_SECRET"
		}
		path := filepath.Join(t.TempDir(), "observation.json")
		if writeBootstrapDiagnostic(path, &bootstrapDiagnosticRecorder{value: value}) == nil {
			t.Fatal("open diagnostic vocabulary accepted")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("invalid observation created output")
		}
	}
}

func TestBootstrapDiagnosticFirstClaimAndConcurrentObservation(t *testing.T) {
	recorder := &bootstrapDiagnosticRecorder{}
	var winners atomic.Int32
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			if recorder.claim(daemonHomeIsolated) {
				winners.Add(1)
			}
		})
	}
	group.Wait()
	if winners.Load() != 1 {
		t.Fatal("first-attempt claim did not win exactly once")
	}
	frames := diagnosticTestFrames(t)
	group.Go(func() {
		_, _ = decodeDescriptorObserved(bytes.NewReader(frames), recorder.observe)
	})
	group.Go(func() {
		for range 100 {
			_ = recorder.snapshot()
		}
	})
	group.Wait()
	recorder.finish(bootstrapDiagnostic{Outcome: "decode_error"}, time.Now(), newBoundedBuffer(64<<10))
	if recorder.claim(daemonHomeLogin) {
		t.Fatal("later attempt overwrote first claim")
	}
	got := recorder.snapshot()
	if got.Policy != daemonHomeIsolated || got.Outcome != "decode_error" || !got.ProgressValidated || !got.SecondFrameComplete {
		t.Fatal("first bootstrap evidence was lost")
	}
}

func TestDiagnosticDaemonHomeFlagsRejectBeforeAppInspection(t *testing.T) {
	for _, args := range [][]string{
		{"--diagnostic-daemon-home=unknown"},
		{"--diagnostic-daemon-home=login", "--deterministic-only"},
		{"--bootstrap-diagnostic=/private/observation.json"},
	} {
		if _, err := parseConfig(args); err == nil || (!strings.Contains(err.Error(), "diagnostic") && !strings.Contains(err.Error(), "HOME")) {
			t.Fatal("diagnostic flags were not rejected before App inspection")
		}
	}
}

func TestDiagnosticDaemonHomeIsolatedEquivalence(t *testing.T) {
	base := []string{"PATH=/usr/bin", "HOME=/Users/disposable", "CFFIXED_USER_HOME=/private/fixture"}
	for _, policy := range []daemonHomePolicy{daemonHomeIsolated} {
		directory := filepath.Join(t.TempDir(), "data")
		want, err := isolatedDaemonEnvironment(base, directory)
		if err != nil {
			t.Fatal(err)
		}
		got, err := diagnosticDaemonEnvironment(base, directory, policy)
		if err != nil || !slices.Equal(got, want) {
			t.Fatal("isolated policy changed")
		}
	}
}

func TestDiagnosticDaemonHomeRejectsInvalidEnvironment(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy daemonHomePolicy
		base   []string
	}{
		{"unknown", "unknown", []string{"HOME=/Users/disposable"}},
		{"missing", daemonHomeLogin, []string{"PATH=/usr/bin"}},
		{"empty", daemonHomeLogin, []string{"HOME="}},
		{"duplicate", daemonHomeLogin, []string{"HOME=/Users/disposable", "HOME=/Users/other"}},
		{"relative", daemonHomeLogin, []string{"HOME=relative"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := diagnosticDaemonEnvironment(test.base, filepath.Join(t.TempDir(), "data"), test.policy); err == nil {
				t.Fatal("expected environment rejection")
			}
		})
	}
	for _, base := range [][]string{{"PATH=/usr/bin"}, {"HOME="}, {"HOME=relative"}, {"HOME=/Users/disposable", "HOME=/Users/other"}} {
		if _, err := diagnosticDaemonEnvironment(base, filepath.Join(t.TempDir(), "data"), ""); err == nil {
			t.Error("normal environment accepted invalid original HOME")
		}
	}
}

func TestDiagnosticDaemonHomeConfigRequiresBoundedDeterministicDiagnostic(t *testing.T) {
	t.Setenv("HOME", "/Users/disposable")
	for _, test := range []struct {
		name   string
		config config
		valid  bool
	}{
		{"default", config{}, true},
		{"isolated", config{diagnosticDaemonHome: "isolated", bootstrapDiagnosticPath: "/private/diagnostic.json", deterministicOnly: true}, true},
		{"isolated without diagnostic", config{diagnosticDaemonHome: "isolated", deterministicOnly: true}, false},
		{"isolated without deterministic", config{diagnosticDaemonHome: "isolated", bootstrapDiagnosticPath: "/private/diagnostic.json"}, false},
		{"normal deterministic diagnostic", config{bootstrapDiagnosticPath: "/private/diagnostic.json", deterministicOnly: true}, true},
		{"login", config{diagnosticDaemonHome: "login", bootstrapDiagnosticPath: "/private/diagnostic.json", deterministicOnly: true}, true},
		{"unknown", config{diagnosticDaemonHome: "other"}, false},
		{"login without diagnostic", config{diagnosticDaemonHome: "login", deterministicOnly: true}, false},
		{"login without deterministic", config{diagnosticDaemonHome: "login", bootstrapDiagnosticPath: "/private/diagnostic.json"}, false},
		{"diagnostic without deterministic", config{bootstrapDiagnosticPath: "/private/diagnostic.json"}, false},
		{"relative path", config{bootstrapDiagnosticPath: "diagnostic.json", deterministicOnly: true}, false},
		{"unclean path", config{bootstrapDiagnosticPath: "/private/../diagnostic.json", deterministicOnly: true}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateBootstrapDiagnosticConfig(test.config)
			if !test.valid && err == nil {
				t.Fatal("expected config rejection")
			}
			if test.valid && err != nil {
				t.Fatal("valid diagnostic config rejected")
			}
		})
	}
	for _, home := range []string{"", "relative"} {
		t.Setenv("HOME", home)
		for _, policy := range []string{"", "login"} {
			if err := validateBootstrapDiagnosticConfig(config{diagnosticDaemonHome: policy, bootstrapDiagnosticPath: "/private/diagnostic.json", deterministicOnly: true}); err == nil {
				t.Error("config accepted invalid original HOME")
			}
		}
	}
}
