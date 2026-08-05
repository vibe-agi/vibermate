package main

import "testing"

func TestParseArgumentsRequiresExplicitHostPathsAndPipe(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		nil,
		{"--app-cache-dir=/tmp/cache"},
		{"--app-cache-dir=/tmp/cache", "--data-dir=/tmp/data"},
		{
			"--app-cache-dir=/tmp/cache",
			"--data-dir=/tmp/data",
			"--webview-origin=https://example.com",
			"--bootstrap-fd=1",
			"--parent-lifetime-fd=0",
		},
		{
			"--app-cache-dir=/tmp/cache",
			"--data-dir=/tmp/data",
			"--webview-origin=tauri://localhost",
			"--bootstrap-fd=1",
		},
		{
			"--app-cache-dir=/tmp/cache",
			"--data-dir=/tmp/data",
			"--webview-origin=tauri://localhost",
			"--bootstrap-fd=1",
			"--parent-lifetime-fd=2",
		},
		{"--unknown=value"},
	} {
		if _, _, err := parseArguments(arguments); err == nil {
			t.Fatalf("parseArguments(%v) succeeded", arguments)
		}
	}
}
