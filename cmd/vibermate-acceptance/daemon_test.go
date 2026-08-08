package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
)

func TestDecodeDescriptorCompletesAtNewlineWithoutWaitingForEOF(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	payload, err := json.Marshal(desktopbootstrap.Descriptor{
		Schema:         desktopbootstrap.DescriptorSchema,
		InstanceID:     "runtime-instance",
		ProcessID:      41,
		BaseURL:        "http://127.0.0.1:41000",
		APIVersions:    []string{"v1"},
		EventVersions:  []string{},
		BootstrapNonce: base64.RawURLEncoding.EncodeToString(make([]byte, capabilityBytes)),
	})
	if err != nil {
		t.Fatal(err)
	}
	progress, err := json.Marshal(desktopbootstrap.RuntimeStartingProgress())
	if err != nil {
		t.Fatal(err)
	}
	decoded := make(chan struct {
		descriptor desktopbootstrap.Descriptor
		err        error
	}, 1)
	go func() {
		descriptor, decodeErr := decodeDescriptor(reader)
		decoded <- struct {
			descriptor desktopbootstrap.Descriptor
			err        error
		}{descriptor: descriptor, err: decodeErr}
	}()
	frames := append(append(progress, '\n'), append(payload, '\n')...)
	if _, err := writer.Write(frames); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-decoded:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.descriptor.InstanceID != "runtime-instance" {
			t.Fatalf("instance ID = %q", result.descriptor.InstanceID)
		}
	case <-time.After(time.Second):
		t.Fatal("descriptor decoder waited for EOF after a complete frame")
	}
}

func TestDaemonInvocationPinsPackagedWebviewOrigin(t *testing.T) {
	t.Parallel()

	arguments := daemonArguments("/private/tmp/cache", "/private/tmp/data")
	if !slices.Contains(
		arguments,
		"--webview-origin=tauri://localhost",
	) {
		t.Fatalf("daemon arguments = %v", arguments)
	}
	if slices.Contains(
		arguments,
		"--webview-origin=http://127.0.0.1:1420",
	) {
		t.Fatalf("daemon arguments enabled development origin = %v", arguments)
	}
	if !slices.Contains(arguments, "--parent-lifetime-fd=0") {
		t.Fatalf("daemon arguments omit parent ownership = %v", arguments)
	}
}

func TestDecodeDescriptorAcceptsOnlyClosedStartupFailure(t *testing.T) {
	t.Parallel()

	progress, err := json.Marshal(desktopbootstrap.RuntimeStartingProgress())
	if err != nil {
		t.Fatal(err)
	}
	failure, err := json.Marshal(desktopbootstrap.StartupFailure(
		desktopbootstrap.FailureStorageSchemaNewer,
	))
	if err != nil {
		t.Fatal(err)
	}
	payload := append(append(progress, '\n'), append(failure, '\n')...)
	_, err = decodeDescriptor(bytes.NewReader(payload))
	if err == nil || !strings.Contains(
		err.Error(),
		"reason=storage_schema_newer",
	) {
		t.Fatalf("typed startup failure error = %v", err)
	}
}
