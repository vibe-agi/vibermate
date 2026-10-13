package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestNativeDiagnosticBufferExactLimitAndContinuedDrain(t *testing.T) {
	for _, size := range []int{65536, 65537, 1048576} {
		buffer := newBoundedBuffer(64 << 10)
		written, err := buffer.Write(bytes.Repeat([]byte("x"), size))
		data, overflow := buffer.snapshot()
		if err != nil || written != size || len(data) != 65536 || overflow != (size > 65536) {
			t.Fatalf("diagnostic size%d: written%d retained%d overflow%t err%v", size, written, len(data), overflow, err)
		}
		if written, err = buffer.Write([]byte("tail")); err != nil || written != 4 || buffer.Len() != 65536 {
			t.Fatal("full diagnostic buffer stopped draining")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary, "-test.run=^TestNativeDiagnosticFloodChild$")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "VIBERMATE_NATIVE_DIAGNOSTIC_CHILD=1"}
	command.WaitDelay = time.Second
	buffer := newBoundedBuffer(64 << 10)
	var output bytes.Buffer
	command.Stderr = buffer
	command.Stdout = &output
	if err = command.Run(); err != nil || output.String() != "drained" {
		t.Fatalf("bounded capture blocked child or lost completion: %v", err)
	}
	data, overflow := buffer.snapshot()
	if !overflow || len(data) != 65536 {
		t.Fatal("flood was not bounded and marked")
	}
}

func TestNativeDiagnosticFloodChild(t *testing.T) {
	if os.Getenv("VIBERMATE_NATIVE_DIAGNOSTIC_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	for i := 0; i < 16; i++ {
		if _, err := os.Stderr.Write(bytes.Repeat([]byte("x"), 65536)); err != nil {
			os.Exit(2)
		}
	}
	_, _ = os.Stdout.WriteString("drained")
	os.Exit(0)
}
