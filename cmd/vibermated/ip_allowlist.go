package main

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/ipallowlist"
)

// runServerIPAllowlist shows or clears the Server IP allowlist from the Server
// machine. Clearing is the way back in when no allowed network can reach the
// Server; a running Server applies it within a few seconds.
func runServerIPAllowlist(arguments []string, stdout, stderr io.Writer) int {
	clear := len(arguments) > 0 && arguments[0] == "clear"
	if clear {
		arguments = arguments[1:]
	}
	command := "ip-allowlist"
	if clear {
		command = "ip-allowlist clear"
	}
	dataDirectory, err := parseServerDataDirectoryArguments(arguments, command)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	directory := filepath.Join(dataDirectory, "server-admin")
	if clear {
		if _, err := ipallowlist.Clear(directory, time.Now); err != nil {
			fmt.Fprintln(stderr, "clear Runtime Server IP allowlist:", err)
			return 1
		}
		fmt.Fprintln(stdout, "IP allowlist cleared: every address may connect.")
		return 0
	}
	snapshot, err := ipallowlist.Read(directory)
	if err != nil {
		fmt.Fprintln(stderr, "read Runtime Server IP allowlist:", err)
		fmt.Fprintln(stderr, "Run `vibermated server ip-allowlist clear` to reset it.")
		return 1
	}
	if snapshot.List.Len() == 0 {
		fmt.Fprintln(stdout, "IP allowlist is empty: every address may connect.")
		return 0
	}
	for _, entry := range snapshot.List.Entries() {
		fmt.Fprintln(stdout, entry)
	}
	return 0
}
