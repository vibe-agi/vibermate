package serverdaemon

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

func TestRecognizedClientProtectionsReadsTheOperatorSwitch(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]clientadapter.ProtectionSwitch{
		"":      clientadapter.ProtectionsOn,
		"on":    clientadapter.ProtectionsOn,
		"off":   clientadapter.ProtectionsOff,
		" OFF ": clientadapter.ProtectionsOff,
		"0":     clientadapter.ProtectionsOff,
		"no":    clientadapter.ProtectionsOff,
	} {
		var report bytes.Buffer
		if got := recognizedClientProtections(value, &report); got != want || report.Len() != 0 {
			t.Fatalf("recognizedClientProtections(%q) = %d, report %q", value, got, report.String())
		}
	}
	// An unrecognized value keeps the protections on and says so once.
	var report bytes.Buffer
	if got := recognizedClientProtections("banana", &report); got != clientadapter.ProtectionsOn {
		t.Fatalf("an unrecognized value gave %d", got)
	}
	line := report.String()
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") ||
		!strings.Contains(line, clientadapter.RecognizedProtectionsVariable+`="banana"`) {
		t.Fatalf("report = %q", line)
	}
}

// ProductionOptions reads the switch from the process environment once.
func TestProductionOptionsCarriesTheOperatorSwitch(t *testing.T) {
	for value, want := range map[string]clientadapter.ProtectionSwitch{
		"off":    clientadapter.ProtectionsOff,
		"on":     clientadapter.ProtectionsOn,
		"banana": clientadapter.ProtectionsOn,
	} {
		t.Setenv(clientadapter.RecognizedProtectionsVariable, value)
		root := t.TempDir()
		factory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(root, "secrets", "store.json"))
		if err != nil {
			t.Fatal(err)
		}
		options, err := ProductionOptions(context.Background(), filepath.Join(root, "data"), "127.0.0.1:0", "", serverhost.TransportOptions{Mode: serverhost.TransportSelfSignedTLS}, factory, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if got := options.Host.Runtime.RecognizedClientProtections; got != want {
			t.Fatalf("%s=%q gave %d, want %d", clientadapter.RecognizedProtectionsVariable, value, got, want)
		}
	}
}
