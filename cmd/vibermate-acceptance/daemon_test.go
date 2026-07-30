package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"slices"
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
	if _, err := writer.Write(append(payload, '\n')); err != nil {
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
}
