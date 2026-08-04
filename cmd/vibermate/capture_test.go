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
		"--name", "Project terminal",
		"--client", "desktop-app",
		"--expires-in", "2h",
		"--yes",
		"--format", "shell",
	})
	if err != nil || config.name != "Project terminal" ||
		config.clientClass != manualcapture.ClientDesktopApp ||
		config.lifetime != manualcapture.LifetimeTemporary ||
		config.expiresIn != 2*time.Hour || !config.yes ||
		config.outputFormat != "shell" {
		t.Fatalf("config=%+v err=%v", config, err)
	}
	for _, arguments := range [][]string{
		{"--name", "Project terminal", "--route", "route-one"},
		{"--name", "Project terminal", "--until-revoked", "--expires-in", "2h"},
		{"--name", "Project terminal", "--expires-in", "30s"},
		{"--name", "Project terminal", "--format", "json"},
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
	context := capturecontrol.ManualCaptureContext{
		ConfirmationToken: "ctx_private-review-token",
		ProxyAddress:      "http://127.0.0.1:32123",
		Root: capturecontrol.RootPublicDelivery{
			Kind:        "local_path",
			DERSHA256:   strings.Repeat("a", 64),
			Fingerprint: "AA:BB:CC",
			PEMPath:     "/private/root.pem",
		},
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
			"AA:BB:CC",
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
