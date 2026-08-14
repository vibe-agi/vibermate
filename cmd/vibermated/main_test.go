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
			"--webview-origin=vibermate://desktop",
			"--bootstrap-fd=1",
		},
		{
			"--app-cache-dir=/tmp/cache",
			"--data-dir=/tmp/data",
			"--webview-origin=vibermate://desktop",
			"--bootstrap-fd=1",
			"--parent-lifetime-fd=2",
		},
		{
			"--app-cache-dir=/tmp/cache",
			"--data-dir=/tmp/data",
			"--webview-origin=tauri://localhost",
			"--bootstrap-fd=1",
			"--parent-lifetime-fd=0",
		},
		{
			"--app-cache-dir=/tmp/cache",
			"--data-dir=/tmp/data",
			"--webview-origin=http://127.0.0.1:1420",
			"--bootstrap-fd=1",
			"--parent-lifetime-fd=0",
		},
		{"--unknown=value"},
	} {
		if _, _, err := parseArguments(arguments); err == nil {
			t.Fatalf("parseArguments(%v) succeeded", arguments)
		}
	}
}

func TestParseArgumentsAcceptsFlutterDesktopOrigin(t *testing.T) {
	t.Parallel()

	config, resources, err := parseArguments([]string{
		"--app-cache-dir=/tmp/cache",
		"--data-dir=/tmp/data",
		"--webview-origin=vibermate://desktop",
		"--bootstrap-fd=1",
		"--parent-lifetime-fd=0",
	})
	if err != nil {
		t.Fatalf("parseArguments() error = %v", err)
	}
	defer resources.bootstrap.Close()
	defer resources.parentLifetime.Close()
	if config.webviewOrigin != "vibermate://desktop" {
		t.Fatalf("webview origin = %q", config.webviewOrigin)
	}
}
