package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/ipallowlist"
)

func TestServerIPAllowlistCommandShowsAndClearsTheList(t *testing.T) {
	t.Parallel()

	dataDirectory := t.TempDir()
	run := func(arguments ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := runServerIPAllowlist(arguments, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	if code, stdout, _ := run("--data-dir", dataDirectory); code != 0 ||
		!strings.Contains(stdout, "every address may connect") {
		t.Fatalf("empty list: code=%d stdout=%q", code, stdout)
	}
	store, err := ipallowlist.Open(filepath.Join(dataDirectory, "server-admin"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	list, err := ipallowlist.Parse([]string{"203.0.113.0/24", "2001:db8::/32"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replace(0, list); err != nil {
		t.Fatal(err)
	}
	if code, stdout, _ := run("--data-dir=" + dataDirectory); code != 0 ||
		stdout != "203.0.113.0/24\n2001:db8::/32\n" {
		t.Fatalf("list: code=%d stdout=%q", code, stdout)
	}
	if code, stdout, _ := run("clear", "--data-dir", dataDirectory); code != 0 ||
		!strings.Contains(stdout, "cleared") {
		t.Fatalf("clear: code=%d stdout=%q", code, stdout)
	}
	if snapshot, err := ipallowlist.Read(filepath.Join(dataDirectory, "server-admin")); err != nil ||
		snapshot.List.Len() != 0 || snapshot.Revision != 2 {
		t.Fatalf("after clear: %+v, %v", snapshot, err)
	}

	if err := os.WriteFile(filepath.Join(dataDirectory, "server-admin", ipallowlist.FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := run("--data-dir", dataDirectory); code != 1 ||
		!strings.Contains(stderr, "ip-allowlist clear") {
		t.Fatalf("broken file: code=%d stderr=%q", code, stderr)
	}
	if code, _, _ := run("clear", "--data-dir", dataDirectory); code != 0 {
		t.Fatalf("clear of a broken file: code=%d", code)
	}
	if code, _, _ := run("--data-dir", "relative/path"); code != 2 {
		t.Fatalf("relative data directory: code=%d", code)
	}
	if code, _, _ := run("add", "203.0.113.0/24"); code != 2 {
		t.Fatalf("unknown argument: code=%d", code)
	}
}
