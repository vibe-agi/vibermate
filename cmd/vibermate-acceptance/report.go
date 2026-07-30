package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const reportSchema = "vibermate.m0-assembly-acceptance/v3"

type checkStatus string

const (
	checkPassed  checkStatus = "passed"
	checkFailed  checkStatus = "failed"
	checkBlocked checkStatus = "blocked"
)

type acceptanceCheck struct {
	ID     string      `json:"id"`
	Status checkStatus `json:"status"`
	Detail string      `json:"detail"`
}

type acceptanceReport struct {
	Schema        string                `json:"schema"`
	StartedAt     time.Time             `json:"startedAt"`
	FinishedAt    time.Time             `json:"finishedAt"`
	Platform      string                `json:"platform"`
	Architecture  string                `json:"architecture"`
	ClaudeVersion string                `json:"claudeVersion"`
	Provenance    *acceptanceProvenance `json:"provenance,omitempty"`
	Status        checkStatus           `json:"status"`
	Checks        []acceptanceCheck     `json:"checks"`
}

func newReport(now time.Time) acceptanceReport {
	return acceptanceReport{
		Schema:        reportSchema,
		StartedAt:     now.UTC(),
		Platform:      "darwin",
		Architecture:  "arm64",
		ClaudeVersion: "2.1.220",
		Status:        checkPassed,
		Checks:        []acceptanceCheck{},
	}
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
	} else if status == checkBlocked && report.Status == checkPassed {
		report.Status = checkBlocked
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

type blockedError struct {
	reason string
}

func (err *blockedError) Error() string {
	return "M0 assembly acceptance blocked: " + err.reason
}
