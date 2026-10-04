package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type nativeMCPClientReport struct {
	Version      string `json:"version"`
	BinarySHA256 string `json:"binarySha256"`
	SourcePin    string `json:"sourcePin"`
}
type nativeMCPCaseReport struct {
	Name               string `json:"name"`
	Client             string `json:"client"`
	Outcome            string `json:"outcome"`
	NativeOutbound     int    `json:"nativeOutbound"`
	ModelOutbound      int    `json:"modelOutbound"`
	CaptureMatched     bool   `json:"captureMatched"`
	EnvironmentMatched bool   `json:"environmentMatched"`
	ActivityMatched    bool   `json:"activityMatched"`
	Reason             string `json:"reason"`
}
type nativeMCPReport struct {
	SourceSHA      string                  `json:"sourceSha"`
	Clients        []nativeMCPClientReport `json:"clients"`
	FixtureDigests map[string]string       `json:"fixtureDigests"`
	Cases          []nativeMCPCaseReport   `json:"cases"`
}

func newNativeMCPReport() nativeMCPReport {
	r := nativeMCPReport{Clients: []nativeMCPClientReport{}, FixtureDigests: map[string]string{}, Cases: []nativeMCPCaseReport{}}
	for _, version := range []string{"0.145.0", "0.159.2", "0.160.0"} {
		for _, name := range []string{"default_call_text", "default_call_image", "large_result_page", "cancel_before_start", "cancel_after_start", "sse_reconnect", "session404_once", "core_restart_uncertain", "off", "review_allow_once", "review_reject", "strict_external", "observe", "disable_start_race"} {
			r.Cases = append(r.Cases, nativeMCPCaseReport{Name: name, Client: version, Outcome: "incomplete", Reason: "prerequisite_failed"})
		}
	}
	for _, name := range []string{"two_profiles_isolation", "resume_fork_background_isolation", "original_auth_three_paths", "claude221220_reader_launcher_regression"} {
		client := "shared"
		if strings.HasPrefix(name, "claude221220") {
			client = "2.1.220"
		}
		r.Cases = append(r.Cases, nativeMCPCaseReport{Name: name, Client: client, Outcome: "incomplete", Reason: "prerequisite_failed"})
	}
	return r
}

func executeNativeMCP(ctx context.Context, c config) (resultErr error) {
	report := newNativeMCPReport()
	defer func() {
		if err := writeNativeMCPReport(c.reportPath, report); err != nil {
			if errors.Is(err, errNativeSourceUnbound) {
				if resultErr == nil {
					resultErr = err
				}
			} else {
				resultErr = errors.New("native acceptance report could not be preserved")
			}
		}
	}()
	if ctx == nil {
		return errors.New("native acceptance requires a context")
	}
	if _, err := validateNativeMCPConfig(c); err != nil {
		return errors.New("native acceptance prerequisites failed")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	directory, err := os.MkdirTemp("", "vibermate-native-acceptance-*")
	if err != nil {
		return errors.New("native acceptance private directory unavailable")
	}
	defer os.RemoveAll(directory)
	clients, err := verifyNativeMCPClients(ctx, c, directory)
	if err != nil {
		for i := range report.Cases {
			report.Cases[i].Reason = "client_identity_mismatch"
		}
		return errors.New("native client identity mismatch")
	}
	for _, client := range clients {
		report.Clients = append(report.Clients, client.nativeMCPClientReport)
	}
	var fixtureDigests map[string]string
	report.SourceSHA, fixtureDigests, err = nativeSourceProvenance(ctx, directory)
	if fixtureDigests != nil {
		report.FixtureDigests = fixtureDigests
	}
	if err != nil {
		// Diagnostic execution is allowed, but no scenario can become PASS
		// evidence when the executable is not tied to clean source.
		report.SourceSHA = ""
	}
	node, err := nativePinnedNode(ctx, directory)
	if err != nil {
		for i := range report.Cases {
			report.Cases[i].Reason = "node_version_mismatch"
		}
		return err
	}
	if err = verifyNativeClientVersions(ctx, clients, node, directory); err != nil {
		for i := range report.Cases {
			report.Cases[i].Reason = "client_version_mismatch"
		}
		return err
	}
	for i := range report.Cases {
		report.Cases[i].Reason = "case_not_implemented"
	}
	for _, client := range clients {
		if client.Version == "2.1.220" {
			continue
		}
		for i, row := range report.Cases {
			if (row.Name != "observe" && row.Name != "off" && row.Name != "strict_external") || row.Client != client.Version {
				continue
			}
			if ctx.Err() != nil {
				report.Cases[i].Reason = "run_deadline"
				continue
			}
			caseContext, caseCancel := context.WithTimeout(ctx, 90*time.Second)
			report.Cases[i], _ = runNativePolicyCase(caseContext, directory, client, node, row.Name)
			caseCancel()
			if err := writeNativeMCPReport(c.reportPath, report); err != nil && !errors.Is(err, errNativeSourceUnbound) {
				return errors.New("native partial report unavailable")
			}
		}
	}
	return errors.New("native acceptance matrix is incomplete")
}

func writeNativeMCPReport(path string, report nativeMCPReport) error {
	report.Cases = append([]nativeMCPCaseReport{}, report.Cases...)
	unbound := false
	if !nativeBuildMatchesSource(report.SourceSHA) {
		report.SourceSHA = ""
		for i := range report.Cases {
			if report.Cases[i].Outcome == "passed" {
				report.Cases[i].Outcome = "incomplete"
				report.Cases[i].Reason = "source_provenance_unavailable"
				unbound = true
			}
		}
	}
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("native report path invalid")
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".native-report-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = file.Chmod(0600); err != nil {
		return err
	}
	if _, err = file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	if unbound {
		return errNativeSourceUnbound
	}
	return nil
}

func validateNativeMCPConfig(c config) (config, error) {
	if c.desktopAppPath != "" || c.daemonPath != "" || c.launcherPath != "" || c.codexPath != "" || c.anthropicKeyPath != "" || c.dataDirectory != "" || c.keepData || c.deterministicOnly {
		return config{}, errors.New("native MCP acceptance requires only isolated client inputs")
	}
	for _, input := range []struct {
		name string
		path *string
	}{
		{"codex145", &c.codex145Path}, {"codex159", &c.codex159Path},
		{"codex160", &c.codex160Path}, {"claude", &c.claudePath},
	} {
		canonical, err := executablePath(*input.path)
		if err != nil {
			return config{}, fmt.Errorf("--%s requires an absolute executable", input.name)
		}
		*input.path = canonical
	}
	if c.reportPath == "" || !filepath.IsAbs(c.reportPath) || filepath.Clean(c.reportPath) != c.reportPath {
		return config{}, errors.New("--report requires an absolute clean path")
	}
	// These are fixed acceptance bounds, independent of packaged-app defaults.
	c.timeout = 20 * time.Minute
	return c, nil
}

// The exact pinned wrappers use Node require.resolve before vendor fallback.
// Acceptance supports their verified hoisted package only, and rejects every
// earlier lookup location instead of assuming the pinned sibling is selected.
func checkNativeWrapperResolution(entrypoint, native string) error {
	if !filepath.IsAbs(entrypoint) || !filepath.IsAbs(native) || filepath.Base(entrypoint) != "codex.js" || filepath.Base(filepath.Dir(entrypoint)) != "bin" || filepath.Base(filepath.Dir(filepath.Dir(entrypoint))) != "codex" {
		return errors.New("unsupported native wrapper layout")
	}
	scope := filepath.Dir(filepath.Dir(filepath.Dir(entrypoint)))
	for _, prefix := range []string{"codex/bin/node_modules", "codex/node_modules", "node_modules"} {
		_, err := os.Lstat(filepath.Join(scope, prefix, "@openai", "codex-darwin-arm64"))
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("native wrapper resolution may be shadowed")
		}
	}
	root, err := filepath.EvalSymlinks(scope)
	if err != nil {
		return errors.New("native package root unavailable")
	}
	packageDirectory, err := filepath.EvalSymlinks(filepath.Join(scope, "codex-darwin-arm64"))
	if err != nil {
		return errors.New("native platform package unavailable")
	}
	metadata, err := filepath.EvalSymlinks(filepath.Join(scope, "codex-darwin-arm64", "package.json"))
	if err != nil || filepath.Dir(metadata) != packageDirectory {
		return errors.New("native platform metadata redirects native child")
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(scope, "codex-darwin-arm64", "vendor", "aarch64-apple-darwin", "bin", "codex"))
	if err != nil {
		return errors.New("native child unavailable")
	}
	actual, err := filepath.EvalSymlinks(native)
	if err != nil || actual != expected {
		return errors.New("native child does not match wrapper")
	}
	relative, err := filepath.Rel(root, expected)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("native child escapes package root")
	}
	return nil
}
