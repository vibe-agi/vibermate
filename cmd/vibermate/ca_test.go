package main

import "testing"

func TestCACommandDetectsPlatformAndKeepsConsentExplicit(t *testing.T) {
	for _, args := range [][]string{
		{"install", "--server", "https://runtime.example:9666"},
		{"status", "--server", "http://host.docker.internal:9666"},
		{"download", "--server", "https://runtime.example:9666", "--out", "root.crt"},
		{"install", "--server", "http://host.docker.internal:9666", "--yes", "--fingerprint", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	} {
		if _, err := parseCA(args); err != nil {
			t.Fatalf("valid CA command: %v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"install", "--server", "http://host.docker.internal:9666", "--yes"},
		{"install", "--server", "https://runtime.example:9666", "--platform", "linux"},
		{"download", "--server", "https://runtime.example:9666"},
		{"status", "--server", "https://runtime.example:9666", "--yes"},
		{"install", "--server", "https://runtime.example:9666", "--fingerprint", "****************************************************************"},
	} {
		if _, err := parseCA(args); err == nil {
			t.Fatalf("invalid CA command accepted: %v", args)
		}
	}
}
