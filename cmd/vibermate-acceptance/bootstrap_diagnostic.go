package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type daemonHomePolicy string

const (
	bootstrapDiagnosticSchema                  = "vibermate.bootstrap-home-diagnostic/v1"
	daemonHomeIsolated        daemonHomePolicy = "isolated"
	daemonHomeLogin           daemonHomePolicy = "login"
)

type bootstrapDiagnosticRecorder struct {
	mu      sync.Mutex
	claimed bool
	value   bootstrapDiagnostic
}

type bootstrapDiagnostic struct {
	Schema               string           `json:"schema"`
	Policy               daemonHomePolicy `json:"policy"`
	Started              bool             `json:"started"`
	HomeMatchesParent    bool             `json:"homeMatchesParent"`
	ParentCFFixedPresent bool             `json:"parentCFFixedPresent"`
	ChildCFFixedPresent  bool             `json:"childCFFixedPresent"`
	ProgressValidated    bool             `json:"progressValidated"`
	SecondFrameComplete  bool             `json:"secondFrameComplete"`
	Outcome              string           `json:"outcome"`
	ElapsedMillis        int64            `json:"elapsedMillis"`
	StderrBytes          int              `json:"stderrBytes"`
	StderrTruncated      bool             `json:"stderrTruncated"`
}

func validateBootstrapDiagnosticConfig(config config) error {
	policy := daemonHomePolicy(config.diagnosticDaemonHome)
	if policy != "" && policy != daemonHomeIsolated && policy != daemonHomeLogin {
		return errors.New("unknown diagnostic daemon HOME policy")
	}
	if policy == daemonHomeLogin && config.bootstrapDiagnosticPath == "" {
		return errors.New("login daemon HOME requires a bootstrap diagnostic")
	}
	if config.bootstrapDiagnosticPath != "" {
		if !config.deterministicOnly {
			return errors.New("bootstrap diagnostic requires deterministic acceptance")
		}
		if !filepath.IsAbs(config.bootstrapDiagnosticPath) || filepath.Clean(config.bootstrapDiagnosticPath) != config.bootstrapDiagnosticPath {
			return errors.New("bootstrap diagnostic must be an absolute clean path")
		}
	}
	if policy == daemonHomeLogin {
		_, err := loginDaemonHome(os.Environ())
		return err
	}
	return nil
}

func diagnosticDaemonEnvironment(base []string, dataDirectory string, policy daemonHomePolicy) ([]string, error) {
	switch policy {
	case "", daemonHomeIsolated:
		return isolatedDaemonEnvironment(base, dataDirectory)
	case daemonHomeLogin:
		if _, err := loginDaemonHome(base); err != nil {
			return nil, err
		}
		result := make([]string, 0, len(base))
		for _, entry := range base {
			if !strings.HasPrefix(entry, "CFFIXED_USER_HOME=") {
				result = append(result, entry)
			}
		}
		return result, nil
	default:
		return nil, errors.New("unknown diagnostic daemon HOME policy")
	}
}

func loginDaemonHome(base []string) (string, error) {
	var home string
	count := 0
	for _, entry := range base {
		if strings.HasPrefix(entry, "HOME=") {
			count++
			home = strings.TrimPrefix(entry, "HOME=")
		}
	}
	if count != 1 || home == "" || !filepath.IsAbs(home) {
		return "", errors.New("login daemon HOME must be one nonempty absolute value")
	}
	return home, nil
}

func initialBootstrapDiagnostic(policy daemonHomePolicy) bootstrapDiagnostic {
	if policy == "" {
		policy = daemonHomeIsolated
	}
	return bootstrapDiagnostic{Schema: bootstrapDiagnosticSchema, Policy: policy, Outcome: "not_started"}
}

func (recorder *bootstrapDiagnosticRecorder) claim(policy daemonHomePolicy) bool {
	if recorder == nil {
		return false
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.claimed {
		return false
	}
	recorder.claimed = true
	recorder.value = initialBootstrapDiagnostic(policy)
	return true
}

func (recorder *bootstrapDiagnosticRecorder) observe(frame int) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	switch frame {
	case 1:
		recorder.value.ProgressValidated = true
	case 2:
		recorder.value.SecondFrameComplete = true
	}
}

func (recorder *bootstrapDiagnosticRecorder) finish(value bootstrapDiagnostic, started time.Time, stderr *boundedBuffer) {
	value.ElapsedMillis = time.Since(started).Milliseconds()
	if stderr != nil {
		value.StderrBytes = stderr.Len()
		_, value.StderrTruncated = stderr.snapshot()
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	value.Schema = recorder.value.Schema
	value.Policy = recorder.value.Policy
	value.ProgressValidated = recorder.value.ProgressValidated
	value.SecondFrameComplete = recorder.value.SecondFrameComplete
	recorder.value = value
}

func (recorder *bootstrapDiagnosticRecorder) snapshot() bootstrapDiagnostic {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.value
}

func (value bootstrapDiagnostic) validate() error {
	if value.Schema != bootstrapDiagnosticSchema || (value.Policy != daemonHomeIsolated && value.Policy != daemonHomeLogin) {
		return errors.New("bootstrap diagnostic schema or policy is invalid")
	}
	switch value.Outcome {
	case "not_started", "start_error", "decode_error", "child_exit", "context_done", "bootstrap_deadline", "exchange_error", "ready":
	default:
		return errors.New("bootstrap diagnostic outcome is invalid")
	}
	if value.ElapsedMillis < 0 || value.StderrBytes < 0 || value.StderrBytes > 64<<10 {
		return errors.New("bootstrap diagnostic counters are invalid")
	}
	return nil
}

func writeBootstrapDiagnostic(path string, recorder *bootstrapDiagnosticRecorder) error {
	if recorder == nil {
		return errors.New("bootstrap diagnostic recorder is missing")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("bootstrap diagnostic must be an absolute clean path")
	}
	value := recorder.snapshot()
	if err := value.validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded)+1 > 4096 {
		return errors.New("bootstrap diagnostic exceeds its size limit")
	}
	if _, err := privateAuditPath(filepath.Dir(path), true, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(encoded, '\n'))
	return errors.Join(writeErr, file.Close())
}

func daemonEnvironmentFact(base []string, name string) (string, bool) {
	for _, entry := range base {
		if strings.HasPrefix(entry, name+"=") {
			return strings.TrimPrefix(entry, name+"="), true
		}
	}
	return "", false
}
