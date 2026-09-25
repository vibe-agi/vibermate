package main

import (
	"context"
	"flag"
	"io"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimedata"
)

func runBackupData(arguments []string) error {
	source, target, err := parseSourceTarget("backup-data", arguments)
	if err != nil {
		return err
	}
	return runtimedata.Backup(
		context.Background(), source, target, time.Now().UTC(),
	)
}

func runRestoreData(arguments []string) error {
	source, target, err := parseSourceTarget("restore-data", arguments)
	if err != nil {
		return err
	}
	return runtimedata.Restore(context.Background(), source, target)
}

func runVerifyBackup(arguments []string) error {
	flags := flag.NewFlagSet("verify-backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || *source == "" {
		return runtimedata.ErrTarget
	}
	return runtimedata.ValidateBackup(context.Background(), *source)
}

func parseSourceTarget(name string, arguments []string) (string, string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "")
	target := flags.String("target", "", "")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 ||
		*source == "" || *target == "" {
		return "", "", runtimedata.ErrTarget
	}
	return *source, *target, nil
}
