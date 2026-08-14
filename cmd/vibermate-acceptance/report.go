package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/acceptancereport"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

const reportSchema = acceptancereport.SchemaV7

type checkStatus = acceptancereport.Status

const (
	checkPassed checkStatus = acceptancereport.StatusPassed
	checkFailed checkStatus = acceptancereport.StatusFailed
)

type acceptanceCheck = acceptancereport.Check
type acceptanceClientReport = acceptancereport.Client
type acceptanceReport acceptancereport.Report

func newReport(now time.Time, client acceptanceClient) acceptanceReport {
	return acceptanceReport{
		Schema:       reportSchema,
		StartedAt:    now.UTC(),
		Platform:     acceptancereport.ExpectedPlatform,
		Architecture: acceptancereport.ExpectedArchitecture,
		Client: acceptanceClientReport{
			ID:      string(client.ID),
			Version: client.Version,
		},
		Status: checkPassed,
		Checks: []acceptanceCheck{},
	}
}

func (report *acceptanceReport) bindClientEvidence(
	evidence clientadapter.Evidence,
) error {
	if report == nil {
		return errors.New("acceptance report is nil")
	}
	if err := evidence.Validate(); err != nil {
		return err
	}
	if report.Client.ID != evidence.ID ||
		report.Client.Version != evidence.Version {
		return errors.New(
			"client adapter evidence does not match the acceptance client",
		)
	}
	cloned := evidence
	report.Client.Adapter = &cloned
	return nil
}

func (report *acceptanceReport) add(
	id string,
	status checkStatus,
	detail string,
) {
	report.Checks = append(report.Checks, acceptanceCheck{
		ID:     id,
		Status: status,
		Detail: detail,
	})
	if status == checkFailed {
		report.Status = checkFailed
	}
}

func writeReport(path string, report acceptanceReport) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("acceptance report path must be absolute and clean")
	}
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode acceptance report: %w", err)
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create acceptance report directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".assembly-report-*")
	if err != nil {
		return fmt.Errorf("create acceptance report temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	fail := func(root error) error {
		_ = temporary.Close()
		return root
	}
	if err := temporary.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := temporary.Write(payload); err != nil {
		return fail(err)
	}
	if err := temporary.Sync(); err != nil {
		return fail(err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish acceptance report: %w", err)
	}
	return nil
}
