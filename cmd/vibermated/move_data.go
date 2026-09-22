package main

import (
	"context"
	"flag"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"io"
)

// Native-shell-only offline operation, not a remote management HTTP endpoint.
func runMoveData(arguments []string) error {
	flags := flag.NewFlagSet("move-data", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "")
	target := flags.String("target", "", "")
	cache := flags.String("app-cache-dir", "", "")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return runtimedata.ErrTarget
	}
	paths, err := runtimepath.FromAppCache(*cache)
	if err != nil {
		return runtimedata.ErrTarget
	}
	guard, err := instanceguard.Acquire(paths.GenerationLock)
	if err != nil {
		return runtimedata.ErrBusy
	}
	defer guard.Release()
	return runtimedata.Copy(context.Background(), *source, *target)
}
