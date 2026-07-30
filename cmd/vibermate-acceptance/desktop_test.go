package main

import (
	"slices"
	"testing"
)

func TestPackagedDesktopInvocationUsesIsolatedHome(t *testing.T) {
	t.Parallel()

	arguments := desktopOpenArguments(
		"/private/tmp/VibeMate.app",
		"/private/tmp/vibermate-home",
	)
	if !slices.Contains(
		arguments,
		"HOME=/private/tmp/vibermate-home",
	) {
		t.Fatalf("Desktop open arguments = %v", arguments)
	}
	if arguments[len(arguments)-1] != "/private/tmp/VibeMate.app" {
		t.Fatalf("Desktop App argument = %q", arguments[len(arguments)-1])
	}
}
