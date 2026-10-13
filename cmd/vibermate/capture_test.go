package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/locales"
)

func TestParseCaptureCreateKeepsRoutingOutOfTheCommand(t *testing.T) {
	t.Parallel()

	config, err := parseCaptureCreate([]string{
		"--env", "work",
		"--name", "Project terminal",
		"--client", "desktop-app",
		"--expires-in", "2h",
		"--yes",
		"--format", "shell",
	})
	if err != nil || config.environmentID != "work" || config.name != "Project terminal" ||
		config.clientClass != manualcapture.ClientDesktopApp ||
		config.lifetime != manualcapture.LifetimeTemporary ||
		config.expiresIn != 2*time.Hour || !config.yes ||
		config.outputFormat != "shell" {
		t.Fatalf("config=%+v err=%v", config, err)
	}
	for _, arguments := range [][]string{
		{"--name", "Project terminal"},
		{"--env", "work", "--name", "Project terminal", "--route", "route-one"},
		{"--env", "work", "--name", "Project terminal", "--until-revoked", "--expires-in", "2h"},
		{"--env", "work", "--name", "Project terminal", "--expires-in", "30s"},
		{"--env", "work", "--name", "Project terminal", "--format", "json"},
	} {
		if _, err := parseCaptureCreate(arguments); err == nil {
			t.Fatalf("parseCaptureCreate(%v) succeeded", arguments)
		}
	}
}

func TestCaptureReviewLocalizesFactsWithoutLeakingInternalState(t *testing.T) {
	t.Parallel()

	catalogs, err := locales.New()
	if err != nil {
		t.Fatal(err)
	}
	root := &capturecontrol.RootPublicDelivery{
		Kind: "local_path", DERSHA256: strings.Repeat("a", 64),
		Fingerprint: "AA:BB:CC", PEMPath: "/private/root.pem",
	}
	context := capturecontrol.ManualCaptureContext{
		ConfirmationToken: "ctx_private-review-token",
		ProxyAddress:      "http://127.0.0.1:32123",
		SOCKSAddress:      "127.0.0.1:32124",
		EnvironmentID:     "work",
		Root:              root,
	}
	config := captureCreateConfig{
		name:        "Project terminal",
		clientClass: manualcapture.ClientCLI,
		lifetime:    manualcapture.LifetimeTemporary,
		expiresIn:   24 * time.Hour,
	}
	for _, locale := range []locales.Locale{
		locales.EnglishUS,
		locales.SimplifiedChinese,
	} {
		var output bytes.Buffer
		if err := renderCaptureReview(catalogs, locale, &output, config, context); err != nil {
			t.Fatal(err)
		}
		message := output.String()
		for _, expected := range []string{
			"Project terminal",
			"http://127.0.0.1:32123",
			"127.0.0.1:32124", "SOCKS5",
			"work", "AA:BB:CC",
			"/private/root.pem",
		} {
			if !strings.Contains(message, expected) {
				t.Fatalf("locale=%s message lacks %q: %s", locale, expected, message)
			}
		}
		for _, forbidden := range []string{
			context.ConfirmationToken,
			"revision",
			"\u4fee\u8ba2",
		} {
			if strings.Contains(message, forbidden) {
				t.Fatalf("locale=%s message contains %q: %s", locale, forbidden, message)
			}
		}
	}
}

func TestCaptureConfirmationDefaultsToNo(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"YES\n", true},
		{"\n", false},
		{"no\n", false},
	} {
		got, err := readConfirmation(strings.NewReader(test.input))
		if err != nil || got != test.want {
			t.Fatalf("readConfirmation(%q)=%v err=%v", test.input, got, err)
		}
	}
}

func TestSOCKSDeliveryHumanCopyAndUnchangedShell(t *testing.T) {
	catalogs, err := locales.New()
	if err != nil {
		t.Fatal(err)
	}
	grant := capturecontrol.ManualCaptureGrant{
		Capture:      capturecontrol.ManualCaptureView{ID: "manual-one"},
		ProxyAddress: "http://127.0.0.1:32123", SOCKSAddress: "127.0.0.1:32124",
		ProxyUsername: "capture", ProxyPassword: "one-time-password", EnvironmentID: "work",
		Root: &capturecontrol.RootPublicDelivery{PEMPath: "/private/root.pem"},
	}
	for _, locale := range []locales.Locale{locales.EnglishUS, locales.SimplifiedChinese} {
		var human, shell bytes.Buffer
		if err := renderCaptureGrant(catalogs, locale, &human, "human", grant); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(human.String(), "SOCKS5") || !strings.Contains(human.String(), "127.0.0.1:32124") || !strings.Contains(human.String(), "capture") || !strings.Contains(human.String(), "UDP") {
			t.Fatalf("missing SOCKS delivery: %s", human.String())
		}
		if err := renderCaptureGrant(catalogs, locale, &shell, "shell", grant); err != nil {
			t.Fatal(err)
		}
		want := "export HTTPS_PROXY='http://capture:one-time-password@127.0.0.1:32123'\nexport HTTP_PROXY='http://capture:one-time-password@127.0.0.1:32123'\nexport NODE_EXTRA_CA_CERTS='/private/root.pem'\nexport SSL_CERT_FILE='/private/root.pem'\n"
		if shell.String() != want {
			t.Fatalf("shell changed: %q", shell.String())
		}
		grant.SOCKSAddress = ""
		var disabled bytes.Buffer
		if err := renderCaptureGrant(catalogs, locale, &disabled, "human", grant); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(disabled.String(), "SOCKS5") {
			t.Fatal("disabled listener advertised")
		}
		grant.SOCKSAddress = "127.0.0.1:32124"
	}
}

func TestSOCKSDeliveryGrantMustMatchReviewedEndpoints(t *testing.T) {
	reviewed := capturecontrol.ManualCaptureContext{ProxyAddress: "http://127.0.0.1:32123", SOCKSAddress: "127.0.0.1:32124", EnvironmentID: "work", LaunchAuthorityDigest: strings.Repeat("a", 64)}
	grant := capturecontrol.ManualCaptureGrant{ProxyAddress: reviewed.ProxyAddress, SOCKSAddress: reviewed.SOCKSAddress, EnvironmentID: reviewed.EnvironmentID, LaunchAuthorityDigest: reviewed.LaunchAuthorityDigest}
	if !captureGrantMatchesReview(grant, reviewed) {
		t.Fatal("equal review rejected")
	}
	for _, address := range []string{"", "127.0.0.1:32125"} {
		grant.SOCKSAddress = address
		if captureGrantMatchesReview(grant, reviewed) {
			t.Errorf("changed SOCKS endpoint %q accepted after review", address)
		}
	}
	grant.SOCKSAddress = reviewed.SOCKSAddress
	grant.ProxyAddress = "http://127.0.0.1:32126"
	if captureGrantMatchesReview(grant, reviewed) {
		t.Fatal("changed HTTP endpoint accepted after review")
	}
}

func TestCapturePromptRejectsNonTerminalInput(t *testing.T) {
	t.Parallel()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if terminalInput(reader) {
		t.Fatal("pipe was accepted as an interactive terminal")
	}
	if !terminalInput(strings.NewReader("yes\n")) {
		t.Fatal("injected interactive reader was rejected")
	}
}
