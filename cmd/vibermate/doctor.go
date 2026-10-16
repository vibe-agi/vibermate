package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"unicode"

	"github.com/vibe-agi/vibermate/internal/clientpath"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/productbuild"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/locales"
)

func parseDoctor(args []string) (statusConfig, error) {
	result := statusConfig{doctor: true}
	seen := map[string]bool{}
	for len(args) > 0 {
		if len(args) < 2 || seen[args[0]] {
			return result, errors.New("doctor flag invalid")
		}
		flag, value := args[0], args[1]
		seen[flag] = true
		args = args[2:]
		switch flag {
		case "--server":
			target, err := serverconnection.ParseTarget(value)
			if err != nil {
				return result, err
			}
			result.server = target
		case "--env":
			id, err := environment.NewEnvironmentID(value)
			if err != nil {
				return result, err
			}
			result.environmentID = id
		default:
			return result, errors.New("doctor flag invalid")
		}
	}
	return result, nil
}

// Metadata is informational, never an assertion of catalog compatibility.
func doctorText(value string) string {
	var out strings.Builder
	for _, r := range value {
		if out.Len() >= 256 {
			break
		}
		if !unicode.IsControl(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func executeDoctor(ctx context.Context, config statusConfig, env []string, out io.Writer) (int, string) {
	if ctx.Err() != nil {
		return 130, ""
	}
	render := func(key string, params map[string]string) bool {
		return renderCLIMessage(env, out, "cli.doctor."+key, params) == nil
	}
	if !render("build", map[string]string{"build": productbuild.Label()}) {
		return 1, reasonRenderFailed
	}
	if !config.server.Valid() {
		return executeLocalDoctor(ctx, config, env, out)
	}
	directory, err := clientpath.DefaultRemoteStateDirectory()
	if err != nil {
		return 1, keyRuntimePath
	}
	transportKey := "cli.doctor.transport.https"
	if config.server.Transport() == serverconnection.TransportHTTP {
		transportKey = "cli.transport.http"
	}
	catalogs, err := locales.New()
	if err != nil {
		return 1, reasonRenderFailed
	}
	transport, err := catalogs.Render(locales.Detect(env), transportKey, nil)
	if err != nil {
		return 1, reasonRenderFailed
	}
	if !render("target", map[string]string{"origin": config.server.Origin(), "transport": transport}) {
		return 1, reasonRenderFailed
	}
	report, err := runlauncher.DiagnoseRemote(ctx, config.server, directory, commandClock{}, config.environmentID)
	if err != nil && ctx.Err() != nil {
		return 130, ""
	}
	code := 0
	show := func(stage string, check runlauncher.DoctorCheck) bool {
		state := "skip"
		if check.Checked {
			state = "pass"
			if check.Reason != "" {
				state = "fail"
				code = 1
			}
		}
		var params map[string]string
		if state == "fail" {
			params = map[string]string{"reason": check.Reason}
			if !render("reason."+check.Reason, nil) {
				return false
			}
		}
		return render(stage+"."+state, params)
	}
	if !show("control", report.Control) {
		return 1, reasonRenderFailed
	}
	if report.Control.Checked && report.Control.Reason == "" {
		snapshot := report.Inspection
		if !render("server", map[string]string{"instance": doctorText(snapshot.InstanceID), "username": doctorText(snapshot.Username), "build": doctorText(snapshot.ProductBuild), "api": doctorText(snapshot.APIVersion)}) {
			return 1, reasonRenderFailed
		}
	}
	if !show("entry", report.Entry) || !show("environment", report.Environment) || !render("scope", nil) {
		return 1, reasonRenderFailed
	}
	return code, ""
}

func executeLocalDoctor(ctx context.Context, config statusConfig, env []string, out io.Writer) (int, string) {
	// Preserve the local runtime/storage/root-trust checks, with diagnostic budgets.
	code, key := executeLocalStatus(ctx, true, env, out)
	if ctx.Err() != nil {
		return 130, ""
	}
	if code != 0 && key != "" {
		failureKey := "cli.doctor.localControlFail"
		if key == "cli.error.localRootUntrusted" {
			failureKey = "cli.doctor.root.fail"
		}
		if renderCLIMessage(env, out, failureKey, nil) != nil {
			return 1, reasonRenderFailed
		}
	}
	if err := renderCLIMessage(env, out, "cli.doctor.localScope", nil); err != nil {
		return 1, reasonRenderFailed
	}
	if config.environmentID != "" && code == 0 && key == "" {
		launcher, err := localDoctorLauncher()
		check, cancel := context.WithTimeout(ctx, runlauncher.DoctorTimeout)
		reason := "environment_catalog"
		if err == nil {
			page, catalogErr := launcher.Environments(check)
			if catalogErr == nil {
				reason = "environment_unavailable"
				for _, item := range page.Items {
					if item.ID == config.environmentID {
						reason = ""
						break
					}
				}
			}
		}
		cancel()
		if ctx.Err() != nil {
			return 130, ""
		}
		stage := "environment.pass"
		if reason != "" {
			stage = "environment.fail"
			code = 1
		}
		if reason != "" {
			if renderCLIMessage(env, out, "cli.doctor.reason."+reason, nil) != nil {
				return 1, reasonRenderFailed
			}
		}
		var params map[string]string
		if reason != "" {
			params = map[string]string{"reason": reason}
		}
		if renderCLIMessage(env, out, "cli.doctor."+stage, params) != nil {
			return 1, reasonRenderFailed
		}
	} else {
		if renderCLIMessage(env, out, "cli.doctor.environment.skip", nil) != nil {
			return 1, reasonRenderFailed
		}
	}
	return code, key
}

func localDoctorLauncher() (*runlauncher.Launcher, error) {
	layout, err := runtimepath.Default()
	if err != nil {
		return nil, err
	}
	discovery, err := localdiscovery.NewFile(layout.CLIControlRecord, commandClock{})
	if err != nil {
		return nil, err
	}
	return runlauncher.New(runlauncher.Config{Discovery: discovery, ControlTimeout: runlauncher.DoctorTimeout, BaseEnvironment: []string{}, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard})
}
