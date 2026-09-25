//go:build darwin || linux

package runlauncher

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
)

// RunACP preserves the editor invocation and joins the existing authenticated
// Capture lifecycle. This is explicitly ACP-only: no HTTP proxy/CA/credential
// or launch-policy overlay is injected into an unverified adapter process.
func (launcher *Launcher) RunACP(ctx context.Context, request ACPLaunchRequest) (int, error) {
	if launcher == nil || ctx == nil || len(request.Command) == 0 || request.Command[0] == "" {
		return 1, errors.New("ACP invocation is required")
	}
	input, okInput := launcher.config.Stdin.(*os.File)
	output, okOutput := launcher.config.Stdout.(*os.File)
	stderr, okError := launcher.config.Stderr.(*os.File)
	if !okInput || !okOutput || !okError {
		return 1, errors.New("ACP requires inherited stdio files")
	}
	cwd, err := launcher.config.Getwd()
	if err != nil {
		return 1, &acpProcessError{"working directory", err}
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return 1, &acpProcessError{"working directory", err}
	}
	executable, err := resolveACPExecutable(request.Command[0], cwd, launcher.config.BaseEnvironment)
	if err != nil {
		return 1, &acpProcessError{"executable resolution", err}
	}
	process := ACPProcessConfig{Executable: executable, Arguments: append([]string{}, request.Command...), Directory: cwd, Environment: append([]string{}, launcher.config.BaseEnvironment...), Stdin: input, Stdout: output, Stderr: stderr, ShutdownTimeout: launcher.config.TerminationTimeout}
	// Login terminals do not register a capture or inspect protocol bytes.
	// The appended authentication args/env are used unchanged, even offline.
	if interactiveACPInput(input) {
		result, err := RunACPProcess(ctx, process)
		return result.ExitCode, err
	}
	create := capturecontrol.CreateRequest{EnvironmentID: environment.SystemTransparentID.String(), CWD: cwd, Command: request.Command, ExecutablePath: executable, RuntimeMetadata: runtimeMetadata(launcher.config.BaseEnvironment)}
	var control *controlClient
	if launcher.config.Remote != nil {
		connection, companion, connectErr := connectRemote(ctx, *launcher.config.Remote, controlTransportTimeout(launcher.config), cwd, request.Command, executable)
		if connectErr != nil {
			return 1, connectErr
		}
		defer connection.close()
		control = connection.control
		create.Companion = companion
		launcher.announceRemoteTrust(connection)
	} else {
		session, loadErr := launcher.config.Discovery.Load()
		if loadErr != nil {
			return 1, ErrRuntimeUnavailable
		}
		control, err = newControlClient(session, controlTransportTimeout(launcher.config))
		if err != nil {
			return 1, ErrRuntimeUnavailable
		}
	}
	defer control.close()
	var grant capturecontrol.LaunchGrant
	err = launcher.callWithin(ctx, launcher.config.CreateTimeout, func(call context.Context) error { var err error; grant, err = control.create(call, create); return err })
	if err != nil {
		return 1, classifyCreateFailure(err)
	}
	captureFinished := false
	defer func() {
		if !captureFinished {
			launcher.finishBestEffort(control, grant)
		}
	}()
	if err = grant.Validate(); err != nil {
		return 1, &acpProcessError{"grant validation", err}
	}
	if err = validateLaunchExecutable(executable, grant.ExecutablePath); err != nil {
		return 1, &acpProcessError{"executable validation", err}
	}
	var receipt acpobservation.Record
	err = launcher.callWithTimeout(ctx, func(call context.Context) error {
		return control.jsonRequest(call, http.MethodPost, runActionPath(grant.Run.ID, "start-acp"), "", grant.RunCapability, capturecontrol.StartACPRequest{RecordContent: request.RecordContent}, http.StatusOK, &receipt)
	})
	if err != nil {
		var failure *ControlFailure
		if errors.As(err, &failure) && failure.Status == http.StatusNotFound {
			return 1, ErrACPUnavailable
		}
		return 1, &acpProcessError{"registration", err}
	}
	if receipt.RunID != grant.Run.ID || receipt.Policy.Validate() != nil || receipt.Expired || receipt.Snapshot.Revision != 1 || receipt.Snapshot.Final {
		return 1, errors.New("ACP registration receipt is invalid")
	}
	observer := acpobservation.NewObserver(receipt.Policy.Mode == environment.ContentRecordingFull && request.RecordContent)
	if receipt.Policy.Mode != environment.ContentRecordingOff {
		process.Observer = observer
	}
	process.OnStart = func(call context.Context, pid int) error {
		return launcher.callWithTimeout(call, func(call context.Context) error { return control.attach(call, grant, pid) })
	}
	childContext, cancelChild := context.WithCancel(ctx)
	defer cancelChild()
	type outcome struct {
		result ACPProcessResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() { result, err := RunACPProcess(childContext, process); finished <- outcome{result, err} }()
	heartbeat := time.NewTicker(launcher.config.HeartbeatInterval)
	defer heartbeat.Stop()
	publish := time.NewTicker(time.Second)
	defer publish.Stop()
	save := func(call context.Context, snapshot acpobservation.Snapshot) error {
		return launcher.callWithTimeout(call, func(call context.Context) error {
			return control.jsonRequest(call, http.MethodPost, runActionPath(grant.Run.ID, "observe-acp"), "", grant.RunCapability, snapshot, http.StatusNoContent, nil)
		})
	}
	var lastRevision uint64 = 1
	var authorityFailure error
	warned := false
	for {
		select {
		case done := <-finished:
			observer.Finish(done.result.ExitCode, done.err != nil)
			if err := save(context.Background(), observer.Snapshot()); err != nil {
				fmt.Fprintln(stderr, "vibermate: ACP finished, but the final observation could not be saved.")
				if done.err == nil {
					done.err = &acpProcessError{"final observation", err}
				}
			}
			if authorityFailure != nil {
				return max(done.result.ExitCode, 1), authorityFailure
			}
			if err := launcher.callWithTimeout(context.Background(), func(call context.Context) error { return control.finish(call, grant) }); err != nil {
				if done.err == nil {
					done.err = &acpProcessError{"Capture completion", err}
				}
			} else {
				captureFinished = true
			}
			return done.result.ExitCode, done.err
		case <-publish.C:
			snapshot := observer.Snapshot()
			if snapshot.Revision == lastRevision {
				continue
			}
			if err := save(childContext, snapshot); err != nil {
				if !warned {
					fmt.Fprintln(stderr, "vibermate: ACP observation is temporarily unavailable; protocol forwarding continues.")
					warned = true
				}
				var failure *ControlFailure
				if errors.As(err, &failure) && failure.Status == http.StatusForbidden {
					authorityFailure = errors.New("ACP Capture authority was revoked")
					cancelChild()
				}
			} else {
				lastRevision = snapshot.Revision
			}
		case <-heartbeat.C:
			if err := launcher.callWithTimeout(childContext, func(call context.Context) error { return control.heartbeat(call, grant) }); err != nil {
				authorityFailure = errors.New("ACP Capture heartbeat failed")
				cancelChild()
			}
		}
	}
}

func resolveACPExecutable(command, cwd string, values []string) (string, error) {
	var candidates []string
	if strings.ContainsRune(command, os.PathSeparator) {
		candidates = []string{command}
	} else {
		var path string
		for _, value := range values {
			if strings.HasPrefix(value, "PATH=") {
				path = strings.TrimPrefix(value, "PATH=")
			}
		}
		for _, directory := range filepath.SplitList(path) {
			// Never silently search the workspace via an empty/relative PATH
			// entry. An intentional local adapter uses ./adapter or an abs path.
			if filepath.IsAbs(directory) {
				candidates = append(candidates, filepath.Join(directory, command))
			}
		}
	}
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(cwd, candidate)
		}
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return filepath.Clean(candidate), nil
		}
	}
	return "", errors.New("ACP agent executable not found in the editor's PATH; configure an absolute path")
}
