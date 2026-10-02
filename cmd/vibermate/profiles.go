package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"io"

	"github.com/vibe-agi/vibermate/internal/clientpath"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
)

func executeProfiles(ctx context.Context, arguments []string, stdout io.Writer) (int, string) {
	flags := flag.NewFlagSet("profiles", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	server := flags.String("server", "", "")
	jsonOutput := flags.Bool("json", false, "")
	if err := flags.Parse(arguments); err != nil || len(flags.Args()) != 0 || !*jsonOutput {
		return 2, keyUsage
	}
	config := runlauncher.Config{}
	if *server != "" {
		target, err := serverconnection.ParseTarget(*server)
		if err != nil {
			return 2, keyUsage
		}
		directory, err := clientpath.DefaultRemoteStateDirectory()
		if err != nil {
			return 1, keyRuntimePath
		}
		config.Remote = &runlauncher.RemoteConfig{
			Target: target, StateDirectory: directory, DisplayName: "vibermate-client",
			Clock: commandClock{}, Random: rand.Reader,
		}
	} else {
		layout, err := runtimepath.Default()
		if err != nil {
			return 1, keyRuntimePath
		}
		discovery, err := localdiscovery.NewFile(layout.CLIControlRecord, commandClock{})
		if err != nil {
			return 1, keyRuntimePath
		}
		config.Discovery = discovery
	}
	launcher, err := runlauncher.New(config)
	if err != nil {
		return 1, keyLaunchFailed
	}
	page, err := launcher.Environments(ctx)
	if err != nil {
		return 1, launchFailureKey(err)
	}
	if err := json.NewEncoder(stdout).Encode(page); err != nil {
		return 1, reasonRenderFailed
	}
	return 0, ""
}
