// Command vibermate-acceptance-verify fail-closes on a missing, unsafe,
// malformed, failed, stale, or mode-mismatched packaged acceptance report.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/vibe-agi/vibermate/internal/acceptancereport"
)

type config struct {
	reportPath            string
	expectedMode          string
	expectedSchema        string
	expectedRevision      string
	expectedClientID      string
	expectedClientVersion string
	sourceRoot            string
	desktopApp            string
	acceptanceExecutable  string
	clientEntrypoint      string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	configuration, err := parseConfig(arguments, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	err = acceptancereport.VerifyFile(
		configuration.reportPath,
		acceptancereport.Expectations{
			Mode:          acceptancereport.Mode(configuration.expectedMode),
			Schema:        configuration.expectedSchema,
			Revision:      configuration.expectedRevision,
			ClientID:      configuration.expectedClientID,
			ClientVersion: configuration.expectedClientVersion,
			Artifacts: acceptancereport.ArtifactCoordinates{
				SourceRoot:           configuration.sourceRoot,
				DesktopApp:           configuration.desktopApp,
				AcceptanceExecutable: configuration.acceptanceExecutable,
				ClientEntrypoint:     configuration.clientEntrypoint,
			},
		},
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(
		stdout,
		"packaged %s acceptance verified: schema=%s revision=%s client=%s@%s\n",
		configuration.expectedMode,
		configuration.expectedSchema,
		configuration.expectedRevision,
		configuration.expectedClientID,
		configuration.expectedClientVersion,
	)
	return 0
}

func parseConfig(arguments []string, stderr io.Writer) (config, error) {
	var parsed config
	flags := flag.NewFlagSet(
		"vibermate-acceptance-verify",
		flag.ContinueOnError,
	)
	flags.SetOutput(stderr)
	flags.StringVar(
		&parsed.reportPath,
		"report",
		"",
		"absolute private v5 or v6 report path",
	)
	flags.StringVar(
		&parsed.expectedMode,
		"expected-mode",
		"",
		"exact expected report mode: deterministic or credentialed",
	)
	flags.StringVar(
		&parsed.expectedSchema,
		"expected-schema",
		"",
		"exact expected report schema",
	)
	flags.StringVar(
		&parsed.expectedRevision,
		"expected-revision",
		"",
		"exact full lowercase Git revision",
	)
	flags.StringVar(
		&parsed.expectedClientID,
		"expected-client-id",
		"",
		"fixed deterministic client ID",
	)
	flags.StringVar(
		&parsed.expectedClientVersion,
		"expected-client-version",
		"",
		"fixed deterministic client version",
	)
	flags.StringVar(
		&parsed.sourceRoot,
		"source-root",
		"",
		"trusted absolute checkout root for current v6 configuration digests",
	)
	flags.StringVar(
		&parsed.desktopApp,
		"desktop-app",
		"",
		"trusted absolute current v6 ViberMate.app path",
	)
	flags.StringVar(
		&parsed.acceptanceExecutable,
		"acceptance-executable",
		"",
		"trusted absolute current v6 acceptance executable path",
	)
	flags.StringVar(
		&parsed.clientEntrypoint,
		"client-entrypoint",
		"",
		"trusted absolute current v6 fixed-client entrypoint",
	)
	if err := flags.Parse(arguments); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, errors.New(
			"vibermate-acceptance-verify does not accept positional arguments",
		)
	}
	if parsed.reportPath == "" ||
		parsed.expectedMode == "" ||
		parsed.expectedSchema == "" ||
		parsed.expectedRevision == "" ||
		parsed.expectedClientID == "" ||
		parsed.expectedClientVersion == "" {
		return config{}, errors.New(
			"--report, --expected-mode, --expected-schema, --expected-revision, --expected-client-id, and --expected-client-version are required",
		)
	}
	if parsed.expectedSchema == acceptancereport.SchemaV6 &&
		(parsed.sourceRoot == "" ||
			parsed.desktopApp == "" ||
			parsed.acceptanceExecutable == "" ||
			parsed.clientEntrypoint == "") {
		return config{}, errors.New(
			"current v6 verification requires --source-root, --desktop-app, --acceptance-executable, and --client-entrypoint",
		)
	}
	return parsed, nil
}
