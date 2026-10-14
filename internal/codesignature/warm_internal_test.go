package codesignature

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWarmPagesReadsTheWholeFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "payload")
	want := make([]byte, 3<<20)
	for i := range want {
		want[i] = byte(i)
	}
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := warmPages(ctx, path); err != nil {
		t.Fatal(err)
	}
}

func TestWarmPagesHonoursCancellation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(path, make([]byte, 8<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := warmPages(ctx, path); err == nil {
		t.Fatal("a cancelled warm reported success")
	}
}
