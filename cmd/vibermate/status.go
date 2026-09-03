package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/locales"
)

type statusConfig struct {
	doctor bool
	server serverconnection.Target
}

func parseStatus(arguments []string) (statusConfig, error) {
	if len(arguments) == 0 || (arguments[0] != "status" && arguments[0] != "doctor") {
		return statusConfig{}, errors.New("status or doctor command is required")
	}
	config := statusConfig{doctor: arguments[0] == "doctor"}
	if len(arguments) == 1 {
		return config, nil
	}
	if len(arguments) != 3 || arguments[1] != "--server" {
		return statusConfig{}, errors.New("status accepts one --server host:port")
	}
	target, err := serverconnection.ParseTarget(arguments[2])
	if err != nil {
		return statusConfig{}, err
	}
	config.server = target
	return config, nil
}

func renderCLIMessage(
	environment []string,
	output io.Writer,
	key string,
	parameters map[string]string,
) error {
	catalogs, err := locales.New()
	if err != nil {
		return err
	}
	message, err := catalogs.Render(
		locales.Detect(environment),
		key,
		parameters,
	)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, message)
	return err
}

func executeLocalStatus(
	ctx context.Context,
	doctor bool,
	environment []string,
	stdout io.Writer,
) (int, string) {
	layout, err := runtimepath.Default()
	if err != nil {
		return 1, keyRuntimePath
	}
	discovery, err := localdiscovery.NewFile(
		layout.CLIControlRecord,
		commandClock{},
	)
	if err != nil {
		return 1, keyRuntimePath
	}
	inspection, err := runlauncher.InspectLocal(ctx, discovery, 5*time.Second)
	if err != nil {
		return 1, keyRuntimeUnavailable
	}
	key := "cli.status.local"
	if doctor {
		key = "cli.doctor.local"
	}
	if err := renderCLIMessage(environment, stdout, key, map[string]string{
		"origin":  inspection.Origin,
		"pid":     fmt.Sprintf("%d", inspection.ProcessID),
		"ready":   fmt.Sprintf("%t", inspection.Ready),
		"api":     inspection.APIVersion,
		"state":   inspection.State,
		"host":    inspection.Host,
		"storage": inspection.Storage,
	}); err != nil {
		return 1, reasonRenderFailed
	}
	if !inspection.Ready || inspection.Storage != "healthy" {
		return 1, ""
	}
	return 0, ""
}

func executeRemoteStatus(
	ctx context.Context,
	doctor bool,
	environment []string,
	stdout io.Writer,
	target serverconnection.Target,
	stateDirectory string,
	clock runlauncher.RemoteClock,
) (int, string) {
	inspection, err := runlauncher.InspectRemote(
		ctx, target, stateDirectory, clock, 5*time.Second,
	)
	if err != nil {
		if errors.Is(err, runlauncher.ErrRemoteLoginRequired) {
			return 1, keyRemoteLoginRequired
		}
		return 1, keyRemoteRuntimeUnavailable
	}
	transportKey := "cli.transport.https"
	if !inspection.Encrypted {
		transportKey = "cli.transport.http"
	}
	catalogs, err := locales.New()
	if err != nil {
		return 1, reasonRenderFailed
	}
	locale := locales.Detect(environment)
	transport, err := catalogs.Render(locale, transportKey, nil)
	if err != nil {
		return 1, reasonRenderFailed
	}
	key := "cli.status.remote"
	if doctor {
		key = "cli.doctor.remote"
	}
	if err := renderCLIMessage(environment, stdout, key, map[string]string{
		"origin": inspection.Origin, "instance": inspection.InstanceID,
		"api": inspection.APIVersion, "username": inspection.Username,
		"transport": transport,
	}); err != nil {
		return 1, reasonRenderFailed
	}
	return 0, ""
}
