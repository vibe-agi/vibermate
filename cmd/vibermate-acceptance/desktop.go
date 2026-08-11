package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

const (
	desktopBundleID                    = runtimepath.ApplicationID
	desktopPreferencesSchema           = "vibermate-workbench-preferences/v1"
	desktopPreferencesStateFile        = "workbench-preferences-v1.json"
	desktopPreferencesRestoreLanguage  = "zh-CN"
	desktopPreferencesRestoreSection   = "settings"
	desktopPreferencesSentinelLanguage = "en-US"
	desktopPreferencesSentinelSection  = "captures"
	desktopPreferencesStateLimit       = 4 << 10
	desktopApplicationPathLimit        = 4 << 10
	// A Desktop that remains registered beyond this bound is user-visible as a
	// hung application. Deeper daemon cleanup budgets must never leak into the
	// AppKit exit callback.
	packagedDesktopExitTimeout = 3 * time.Second
)

var errDesktopProcessUnavailable = errors.New("Desktop process is unavailable")

type wallClock struct{}

func (wallClock) Now() time.Time {
	return time.Now().UTC()
}

func exercisePackagedDesktopShell(
	ctx context.Context,
	appPath string,
	layout runtimepath.Layout,
	homeDirectory string,
) error {
	if homeDirectory == "" ||
		!filepath.IsAbs(homeDirectory) ||
		filepath.Clean(homeDirectory) != homeDirectory {
		return errors.New("packaged Desktop home directory is invalid")
	}
	staleEnvironmentID, err := newDesktopPreferencesStaleEnvironmentID()
	if err != nil {
		return fmt.Errorf("create packaged Desktop preference identity: %w", err)
	}
	statePath := desktopPreferencesStatePath(homeDirectory)
	restoredEnvironmentID := string(environment.SystemTransparentID)
	restoredEndpointID := upstreamendpoint.AnthropicOfficialID.String()
	expected := canonicalDesktopPreferences(
		desktopPreferencesRestoreLanguage,
		desktopPreferencesRestoreSection,
		&restoredEnvironmentID,
		&restoredEndpointID,
	)
	seed, err := publishDesktopPreferencesFixture(
		statePath,
		nonCanonicalDesktopPreferences(
			desktopPreferencesRestoreLanguage,
			desktopPreferencesRestoreSection,
			&staleEnvironmentID,
			nil,
		),
	)
	if err != nil {
		return fmt.Errorf("seed packaged Desktop preferences: %w", err)
	}

	var firstMounted desktopPreferencesFile
	var sentinel desktopPreferencesFile
	err = exercisePackagedDesktopLaunch(
		ctx,
		appPath,
		layout,
		homeDirectory,
		func(observe context.Context, done <-chan error) error {
			var observeErr error
			firstMounted, observeErr = waitForDesktopPreferencesRewrite(
				observe,
				done,
				statePath,
				seed,
				expected,
			)
			if observeErr != nil {
				return fmt.Errorf("observe first packaged workbench restore: %w", observeErr)
			}
			sentinel, observeErr = publishDesktopPreferencesFixture(
				statePath,
				canonicalDesktopPreferences(
					desktopPreferencesSentinelLanguage,
					desktopPreferencesSentinelSection,
					nil,
					nil,
				),
			)
			if observeErr != nil {
				return fmt.Errorf("publish packaged preference exit sentinel: %w", observeErr)
			}
			return nil
		},
	)
	if err != nil {
		return err
	}
	firstExit, err := requireDesktopPreferencesRewrite(
		statePath,
		sentinel,
		expected,
	)
	if err != nil {
		return fmt.Errorf("verify first packaged preference exit flush: %w", err)
	}
	if os.SameFile(seed.info, firstMounted.info) {
		return errors.New("first packaged workbench restore did not atomically rewrite preferences")
	}

	var secondSentinel desktopPreferencesFile
	err = exercisePackagedDesktopLaunch(
		ctx,
		appPath,
		layout,
		homeDirectory,
		func(observe context.Context, done <-chan error) error {
			var publishErr error
			secondSentinel, publishErr = publishDesktopPreferencesFixture(
				statePath,
				canonicalDesktopPreferences(
					desktopPreferencesSentinelLanguage,
					desktopPreferencesSentinelSection,
					nil,
					nil,
				),
			)
			if publishErr != nil {
				return fmt.Errorf("publish second packaged preference exit sentinel: %w", publishErr)
			}
			return nil
		},
	)
	if err != nil {
		return err
	}
	if os.SameFile(firstExit.info, secondSentinel.info) {
		return errors.New("second packaged preference sentinel was not atomically published")
	}
	if _, err := requireDesktopPreferencesRewrite(
		statePath,
		secondSentinel,
		expected,
	); err != nil {
		return fmt.Errorf("verify second packaged preference exit flush: %w", err)
	}
	return nil
}

type desktopObservation func(context.Context, <-chan error) error

func exercisePackagedDesktopLaunch(
	ctx context.Context,
	appPath string,
	layout runtimepath.Layout,
	homeDirectory string,
	observe desktopObservation,
) error {
	if observe == nil {
		return errors.New("packaged Desktop observation is unavailable")
	}
	canonicalAppPath, err := canonicalDesktopBundlePath(appPath)
	if err != nil {
		return fmt.Errorf("inspect packaged Desktop App: %w", err)
	}
	runningApplications, err := desktopApplicationPIDs(ctx)
	if err != nil {
		return fmt.Errorf("inspect packaged Desktop application instances: %w", err)
	}
	if len(runningApplications) != 0 {
		return errors.New("another ViberMate Desktop application is already running")
	}
	discovery, err := localdiscovery.NewFile(
		layout.CLIControlRecord,
		wallClock{},
	)
	if err != nil {
		return err
	}
	previous, previousErr := discovery.Load()
	if previousErr != nil &&
		!errors.Is(previousErr, os.ErrNotExist) &&
		!errors.Is(previousErr, localdiscovery.ErrExpired) {
		return fmt.Errorf("inspect prior Desktop generation: %w", previousErr)
	}

	command := exec.Command(
		"/usr/bin/open",
		desktopOpenArguments(canonicalAppPath, homeDirectory)...,
	)
	command.Env = acceptanceEnvironment(os.Environ())
	stdout := newBoundedBuffer(64 << 10)
	stderr := newBoundedBuffer(64 << 10)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start packaged Desktop app: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	var generation localdiscovery.Session
	var desktopApplication desktopApplicationIdentity
	var desktopGuardian *desktopApplicationGuardian
	cleaned := false
	defer func() {
		if desktopGuardian != nil {
			defer desktopGuardian.close()
		}
		if cleaned {
			return
		}
		cleanupPackagedDesktopApplication(desktopGuardian)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}()

	startupContext, cancelStartup := context.WithTimeout(ctx, 30*time.Second)
	defer cancelStartup()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for generation.InstanceID == "" {
		current, loadErr := discovery.Load()
		if loadErr == nil &&
			current.InstanceID != "" &&
			current.InstanceID != previous.InstanceID {
			generation = current
			break
		}
		if loadErr != nil &&
			!errors.Is(loadErr, os.ErrNotExist) &&
			!errors.Is(loadErr, localdiscovery.ErrExpired) {
			return fmt.Errorf("load packaged Desktop generation: %w", loadErr)
		}
		select {
		case waitErr := <-done:
			return prematureDesktopExit("before discovery", waitErr)
		case <-ticker.C:
		case <-startupContext.Done():
			return errors.New("packaged Desktop discovery deadline exceeded")
		}
	}
	desktopApplication, err = waitForPackagedDesktopApplication(
		startupContext,
		done,
		canonicalAppPath,
		generation.ProcessID,
	)
	if err != nil {
		return err
	}
	desktopGuardian, err = startDesktopApplicationGuardian(
		startupContext,
		desktopApplication,
	)
	if err != nil {
		return err
	}
	boundSidecar, err := inspectDesktopProcess(generation.ProcessID)
	if err != nil ||
		boundSidecar.parentID != desktopApplication.ProcessID ||
		boundSidecar.started != desktopApplication.sidecarStarted {
		return errors.New("packaged Desktop process relationship changed after binding")
	}

	if err := observe(startupContext, done); err != nil {
		return err
	}
	current, err := discovery.Load()
	if err != nil || current.InstanceID != generation.InstanceID {
		return errors.New("packaged Desktop generation did not remain stable")
	}
	process, err := inspectDesktopProcess(generation.ProcessID)
	if err != nil || process.parentID != desktopApplication.ProcessID ||
		process.started != desktopApplication.sidecarStarted {
		return errors.New("packaged Desktop generation changed application owner")
	}
	if err := requestDesktopQuit(ctx, desktopGuardian); err != nil {
		return err
	}
	exitContext, cancelExit := context.WithTimeout(ctx, packagedDesktopExitTimeout)
	defer cancelExit()
	select {
	case waitErr := <-done:
		if waitErr != nil {
			return fmt.Errorf(
				"packaged Desktop graceful exit: %w",
				normalizeWaitError(waitErr),
			)
		}
	case <-exitContext.Done():
		return errors.New("packaged Desktop graceful exit deadline exceeded")
	}
	registered, err := desktopApplicationRegistered(
		exitContext,
		desktopApplication,
	)
	if err != nil {
		return fmt.Errorf("inspect packaged Desktop after exit: %w", err)
	}
	if registered {
		return errors.New("packaged Desktop process remained registered after exit")
	}
	for {
		sidecar, inspectErr := inspectDesktopProcess(generation.ProcessID)
		sidecarPresent, inspectErr := desktopProcessIdentityPresent(
			desktopApplication.sidecarStarted,
			sidecar,
			inspectErr,
		)
		if inspectErr != nil {
			return fmt.Errorf("inspect packaged Desktop sidecar after exit: %w", inspectErr)
		}
		removed, err := discoveryRemoved(layout.CLIControlRecord)
		if err != nil {
			return err
		}
		if !sidecarPresent && removed {
			cleaned = true
			return nil
		}
		select {
		case <-ticker.C:
		case <-exitContext.Done():
			if sidecarPresent {
				return errors.New("packaged Desktop sidecar remained after exit")
			}
			return errors.New("packaged Desktop left owned discovery after exit")
		}
	}
}

type desktopWorkbenchPreferences struct {
	Schema                      string  `json:"schema"`
	Language                    string  `json:"language"`
	Section                     string  `json:"section"`
	SelectedCaptureKey          *string `json:"selectedCaptureKey"`
	SelectedConversationKey     *string `json:"selectedConversationKey"`
	SelectedEnvironmentID       *string `json:"selectedEnvironmentId"`
	SelectedEnvironmentRevision *int    `json:"selectedEnvironmentRevision"`
	SelectedEndpointID          *string `json:"selectedEndpointId"`
}

type desktopPreferencesFile struct {
	encoded []byte
	info    os.FileInfo
}

type desktopProcessStart struct {
	seconds      int64
	microseconds int64
}

type desktopProcessSnapshot struct {
	parentID int
	started  desktopProcessStart
}

func desktopProcessIdentityPresent(
	expected desktopProcessStart,
	observed desktopProcessSnapshot,
	inspectErr error,
) (bool, error) {
	if expected.seconds <= 0 {
		return false, errors.New("Desktop process birth identity is invalid")
	}
	if errors.Is(inspectErr, errDesktopProcessUnavailable) {
		return false, nil
	}
	if inspectErr != nil {
		return false, inspectErr
	}
	return observed.started == expected, nil
}

type desktopApplicationIdentity struct {
	ProcessID      int    `json:"processId"`
	BundlePath     string `json:"bundlePath"`
	ExecutablePath string `json:"executablePath"`
	started        desktopProcessStart
	sidecarStarted desktopProcessStart
}

// A PID is not a stable application identity. The guardian brackets its bind
// with kern.proc birth checks, then deliberately keeps one NSRunningApplication
// object alive for every graceful or forced action. If that bind cannot be
// proven, cleanup leaves the process untouched.
type desktopApplicationGuardian struct {
	application desktopApplicationIdentity
	closed      bool
	done        chan error
	input       io.WriteCloser
	lock        sync.Mutex
	output      *bufio.Reader
}

func desktopPreferencesStatePath(homeDirectory string) string {
	return filepath.Join(
		homeDirectory,
		"Library",
		"Application Support",
		desktopBundleID,
		"ui-state",
		desktopPreferencesStateFile,
	)
}

func canonicalDesktopPreferences(
	language string,
	section string,
	environmentID *string,
	endpointID *string,
) []byte {
	encoded, err := json.Marshal(desktopWorkbenchPreferences{
		Schema:                desktopPreferencesSchema,
		Language:              language,
		Section:               section,
		SelectedEnvironmentID: environmentID,
		SelectedEndpointID:    endpointID,
	})
	if err != nil {
		panic("fixed Desktop preference fixture could not be encoded")
	}
	return encoded
}

func nonCanonicalDesktopPreferences(
	language string,
	section string,
	environmentID *string,
	endpointID *string,
) []byte {
	encoded, err := json.MarshalIndent(desktopWorkbenchPreferences{
		Schema:                desktopPreferencesSchema,
		Language:              language,
		Section:               section,
		SelectedEnvironmentID: environmentID,
		SelectedEndpointID:    endpointID,
	}, "", "  ")
	if err != nil {
		panic("fixed Desktop preference fixture could not be encoded")
	}
	return append(encoded, '\n')
}

func newDesktopPreferencesStaleEnvironmentID() (string, error) {
	var identity [16]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return "", err
	}
	return "acceptance." + hex.EncodeToString(identity[:]), nil
}

func publishDesktopPreferencesFixture(
	path string,
	encoded []byte,
) (desktopPreferencesFile, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		len(encoded) == 0 || len(encoded) > desktopPreferencesStateLimit {
		return desktopPreferencesFile{}, errors.New("Desktop preference fixture is invalid")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return desktopPreferencesFile{}, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return desktopPreferencesFile{}, err
	}
	if err := requirePrivateDesktopPreferencesDirectory(directory); err != nil {
		return desktopPreferencesFile{}, err
	}
	if _, err := os.Lstat(path); err == nil {
		if _, readErr := readDesktopPreferencesFile(path); readErr != nil {
			return desktopPreferencesFile{}, readErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return desktopPreferencesFile{}, err
	}
	temporary, err := os.CreateTemp(directory, ".acceptance-preferences-*")
	if err != nil {
		return desktopPreferencesFile{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	fail := func(root error) (desktopPreferencesFile, error) {
		_ = temporary.Close()
		return desktopPreferencesFile{}, root
	}
	if err := temporary.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		return fail(err)
	}
	if err := temporary.Sync(); err != nil {
		return fail(err)
	}
	if err := temporary.Close(); err != nil {
		return desktopPreferencesFile{}, err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return desktopPreferencesFile{}, err
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return desktopPreferencesFile{}, err
	}
	if err := directoryFile.Sync(); err != nil {
		_ = directoryFile.Close()
		return desktopPreferencesFile{}, err
	}
	if err := directoryFile.Close(); err != nil {
		return desktopPreferencesFile{}, err
	}
	return readDesktopPreferencesFile(path)
}

func waitForDesktopPreferencesRewrite(
	ctx context.Context,
	done <-chan error,
	path string,
	previous desktopPreferencesFile,
	expected []byte,
) (desktopPreferencesFile, error) {
	if ctx == nil || done == nil || previous.info == nil {
		return desktopPreferencesFile{}, errors.New("Desktop preference observation is invalid")
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var lastObserved []byte
	for {
		current, err := readDesktopPreferencesFile(path)
		if err != nil {
			return desktopPreferencesFile{}, err
		}
		lastObserved = bytes.Clone(current.encoded)
		if bytes.Equal(current.encoded, expected) &&
			!os.SameFile(previous.info, current.info) {
			return current, nil
		}
		select {
		case waitErr := <-done:
			return desktopPreferencesFile{}, prematureDesktopExit(
				"before workbench preference restore",
				waitErr,
			)
		case <-ticker.C:
		case <-ctx.Done():
			return desktopPreferencesFile{}, fmt.Errorf(
				"packaged Desktop preference restore deadline exceeded; observed=%q",
				lastObserved,
			)
		}
	}
}

func requireDesktopPreferencesRewrite(
	path string,
	previous desktopPreferencesFile,
	expected []byte,
) (desktopPreferencesFile, error) {
	current, err := readDesktopPreferencesFile(path)
	if err != nil {
		return desktopPreferencesFile{}, err
	}
	if previous.info == nil ||
		!bytes.Equal(current.encoded, expected) ||
		os.SameFile(previous.info, current.info) {
		return desktopPreferencesFile{}, errors.New(
			"packaged Desktop preferences were not atomically flushed",
		)
	}
	return current, nil
}

func desktopOpenArguments(
	appPath string,
	homeDirectory string,
) []string {
	return []string{
		"-n",
		"-F",
		"-W",
		"--env",
		"HOME=" + homeDirectory,
		appPath,
	}
}

func prematureDesktopExit(stage string, waitErr error) error {
	if waitErr == nil {
		return fmt.Errorf(
			"packaged Desktop exited successfully %s",
			stage,
		)
	}
	return fmt.Errorf(
		"packaged Desktop exited %s: %w",
		stage,
		normalizeWaitError(waitErr),
	)
}

func canonicalDesktopBundlePath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("Desktop App path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("Desktop App path is not a directory")
	}
	return filepath.Clean(resolved), nil
}

func canonicalDesktopExecutablePath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("Desktop executable path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("Desktop executable path is not executable")
	}
	return filepath.Clean(resolved), nil
}

func requestDesktopQuit(
	ctx context.Context,
	guardian *desktopApplicationGuardian,
) error {
	return guardian.action(ctx, "terminate")
}

func startDesktopApplicationGuardian(
	ctx context.Context,
	application desktopApplicationIdentity,
) (*desktopApplicationGuardian, error) {
	if ctx == nil || application.ProcessID <= 0 ||
		application.started.seconds <= 0 {
		return nil, errors.New("Desktop guardian requires a bound process identity")
	}
	process, err := inspectDesktopProcess(application.ProcessID)
	if err != nil || process.started != application.started {
		return nil, errors.New("Desktop process changed before guardian binding")
	}
	command := exec.Command(
		"/usr/bin/osascript",
		"-l",
		"JavaScript",
		"-e",
		desktopApplicationGuardianScript(application),
	)
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	command.Stderr = newBoundedBuffer(16 << 10)
	if err := command.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, fmt.Errorf("start packaged Desktop process guardian: %w", err)
	}
	guardian := &desktopApplicationGuardian{
		application: application,
		done:        make(chan error, 1),
		input:       input,
		output:      bufio.NewReaderSize(output, 1024),
	}
	go func() {
		guardian.done <- command.Wait()
	}()
	ready, err := guardian.readLine(ctx)
	if err != nil || ready != "ready\n" {
		guardian.close()
		return nil, errors.New("packaged Desktop process guardian did not bind")
	}
	process, err = inspectDesktopProcess(application.ProcessID)
	if err != nil || process.started != application.started {
		guardian.close()
		return nil, errors.New("Desktop process changed during guardian binding")
	}
	return guardian, nil
}

func (guardian *desktopApplicationGuardian) action(
	ctx context.Context,
	action string,
) error {
	if guardian == nil || ctx == nil ||
		(action != "terminate" && action != "force") {
		return errors.New("Desktop guardian action is invalid")
	}
	guardian.lock.Lock()
	defer guardian.lock.Unlock()
	if guardian.closed {
		return errors.New("Desktop process guardian is closed")
	}
	if _, err := io.WriteString(guardian.input, action+"\n"); err != nil {
		return errors.New("Desktop process guardian input is unavailable")
	}
	response, err := guardian.readLineLocked(ctx)
	if err != nil {
		return err
	}
	if response != "accepted\n" {
		return errors.New("Desktop application refused the requested action")
	}
	return nil
}

func (guardian *desktopApplicationGuardian) readLine(ctx context.Context) (
	string,
	error,
) {
	guardian.lock.Lock()
	defer guardian.lock.Unlock()
	return guardian.readLineLocked(ctx)
}

func (guardian *desktopApplicationGuardian) readLineLocked(
	ctx context.Context,
) (string, error) {
	if guardian.closed {
		return "", errors.New("Desktop process guardian is closed")
	}
	result := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		line, err := guardian.output.ReadString('\n')
		result <- struct {
			line string
			err  error
		}{line: line, err: err}
	}()
	select {
	case read := <-result:
		if read.err != nil || len(read.line) > 128 {
			return "", errors.New("Desktop process guardian output is invalid")
		}
		return read.line, nil
	case <-ctx.Done():
		guardian.stopLocked()
		return "", errors.New("Desktop process guardian deadline exceeded")
	}
}

func (guardian *desktopApplicationGuardian) close() {
	if guardian == nil {
		return
	}
	guardian.lock.Lock()
	defer guardian.lock.Unlock()
	guardian.stopLocked()
}

func (guardian *desktopApplicationGuardian) stopLocked() {
	if guardian.closed {
		return
	}
	guardian.closed = true
	_ = guardian.input.Close()
	select {
	case <-guardian.done:
		return
	case <-time.After(2 * time.Second):
	}
}

func desktopApplications(ctx context.Context) ([]desktopApplicationIdentity, error) {
	if ctx == nil {
		return nil, errors.New("Desktop application inspection context is nil")
	}
	inspectionContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(
		inspectionContext,
		"/usr/bin/osascript",
		"-l",
		"JavaScript",
		"-e",
		desktopApplicationsScript(),
	)
	stdout := newBoundedBuffer(16 << 10)
	command.Stdout = stdout
	command.Stderr = newBoundedBuffer(16 << 10)
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("query Desktop application instances: %w", err)
	}
	payload, overflow := stdout.snapshot()
	if overflow {
		return nil, errors.New("Desktop application instance output exceeded its bound")
	}
	return parseDesktopApplications(payload)
}

func desktopApplicationPIDs(ctx context.Context) ([]int, error) {
	applications, err := desktopApplications(ctx)
	if err != nil {
		return nil, err
	}
	processIDs := make([]int, 0, len(applications))
	for _, application := range applications {
		processIDs = append(processIDs, application.ProcessID)
	}
	return processIDs, nil
}

func waitForPackagedDesktopApplication(
	ctx context.Context,
	done <-chan error,
	appPath string,
	sidecarProcessID int,
) (desktopApplicationIdentity, error) {
	if ctx == nil || done == nil || sidecarProcessID <= 0 {
		return desktopApplicationIdentity{}, errors.New(
			"packaged Desktop application observation is invalid",
		)
	}
	sidecar, err := inspectDesktopProcess(sidecarProcessID)
	if err != nil || sidecar.parentID <= 0 {
		return desktopApplicationIdentity{}, errors.New(
			"packaged Desktop sidecar parent is unavailable",
		)
	}
	parentProcessID := sidecar.parentID
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		applications, err := desktopApplications(ctx)
		if err != nil {
			return desktopApplicationIdentity{}, err
		}
		application, matchErr := selectPackagedDesktopApplication(
			applications,
			parentProcessID,
			appPath,
		)
		if application.ProcessID > 0 {
			parent, inspectErr := inspectDesktopProcess(parentProcessID)
			currentSidecar, sidecarErr := inspectDesktopProcess(sidecarProcessID)
			if inspectErr != nil || sidecarErr != nil ||
				currentSidecar.parentID != parentProcessID ||
				currentSidecar.started != sidecar.started {
				return desktopApplicationIdentity{}, errors.New(
					"packaged Desktop process relationship changed during binding",
				)
			}
			application.started = parent.started
			application.sidecarStarted = sidecar.started
		}
		if matchErr != nil {
			return application, matchErr
		}
		if application.ProcessID > 0 {
			return application, nil
		}
		select {
		case waitErr := <-done:
			return desktopApplicationIdentity{}, prematureDesktopExit(
				"before exact application registration",
				waitErr,
			)
		case <-ticker.C:
		case <-ctx.Done():
			return desktopApplicationIdentity{}, errors.New(
				"packaged Desktop application registration deadline exceeded",
			)
		}
	}
}

func selectPackagedDesktopApplication(
	applications []desktopApplicationIdentity,
	parentProcessID int,
	appPath string,
) (desktopApplicationIdentity, error) {
	for _, application := range applications {
		if application.ProcessID != parentProcessID {
			continue
		}
		bundlePath, err := canonicalDesktopBundlePath(application.BundlePath)
		if err != nil || bundlePath != appPath {
			return desktopApplicationIdentity{}, errors.New(
				"packaged Desktop sidecar parent belongs to a different App",
			)
		}
		executablePath, err := canonicalDesktopExecutablePath(
			application.ExecutablePath,
		)
		if err != nil {
			return desktopApplicationIdentity{}, err
		}
		executableRoot := filepath.Join(bundlePath, "Contents", "MacOS")
		relative, err := filepath.Rel(executableRoot, executablePath)
		if err != nil || relative == "." || filepath.Dir(relative) != "." {
			return desktopApplicationIdentity{}, errors.New(
				"packaged Desktop executable is outside the selected App",
			)
		}
		if len(applications) != 1 {
			return application, errors.New(
				"packaged Desktop launch overlapped another application instance",
			)
		}
		return application, nil
	}
	return desktopApplicationIdentity{}, nil
}

func desktopApplicationRegistered(
	ctx context.Context,
	expected desktopApplicationIdentity,
) (bool, error) {
	if expected.ProcessID <= 0 || expected.started.seconds <= 0 {
		return false, errors.New("Desktop application identity is incomplete")
	}
	process, err := inspectDesktopProcess(expected.ProcessID)
	if err != nil || process.started != expected.started {
		return false, nil
	}
	applications, err := desktopApplications(ctx)
	if err != nil {
		return false, err
	}
	for _, application := range applications {
		if application.ProcessID == expected.ProcessID &&
			application.BundlePath == expected.BundlePath &&
			application.ExecutablePath == expected.ExecutablePath {
			return true, nil
		}
	}
	return false, nil
}

func cleanupPackagedDesktopApplication(guardian *desktopApplicationGuardian) {
	if guardian == nil {
		return
	}
	application := guardian.application
	quitContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = requestDesktopQuit(quitContext, guardian)
	cancel()
	waitContext, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		registered, err := desktopApplicationRegistered(waitContext, application)
		if err != nil {
			return
		}
		if !registered {
			return
		}
		select {
		case <-ticker.C:
		case <-waitContext.Done():
			forceContext, forceCancel := context.WithTimeout(
				context.Background(),
				5*time.Second,
			)
			_ = guardian.action(forceContext, "force")
			forceCancel()
			return
		}
	}
}

func parseDesktopApplications(payload []byte) ([]desktopApplicationIdentity, error) {
	var applications []desktopApplicationIdentity
	if err := decodeClosedJSON(payload, &applications); err != nil {
		return nil, errors.New("Desktop application identity output is invalid")
	}
	if applications == nil || len(applications) > 16 {
		return nil, errors.New("Desktop application identity count is invalid")
	}
	seen := make(map[int]struct{}, len(applications))
	for _, application := range applications {
		if application.ProcessID <= 0 ||
			!validDesktopApplicationPath(application.BundlePath) ||
			!validDesktopApplicationPath(application.ExecutablePath) {
			return nil, errors.New("Desktop application process identity is invalid")
		}
		if _, duplicate := seen[application.ProcessID]; duplicate {
			return nil, errors.New("Desktop application process identity is duplicated")
		}
		seen[application.ProcessID] = struct{}{}
	}
	return applications, nil
}

func validDesktopApplicationPath(path string) bool {
	return path != "" && len(path) <= desktopApplicationPathLimit &&
		filepath.IsAbs(path) && filepath.Clean(path) == path &&
		!strings.ContainsRune(path, '\x00')
}

func desktopApplicationsScript() string {
	return desktopApplicationsScriptForBundle(desktopBundleID)
}

func desktopApplicationsScriptForBundle(bundleID string) string {
	return `ObjC.import("AppKit");
var applications = $.NSRunningApplication.runningApplicationsWithBundleIdentifier(` +
		strconv.Quote(bundleID) + `);
var identities = [];
for (var index = 0; index < applications.count; index++) {
  var application = applications.objectAtIndex(index);
  identities.push({
    processId: Number(ObjC.unwrap(application.processIdentifier)),
    bundlePath: ObjC.unwrap(application.bundleURL.path),
    executablePath: ObjC.unwrap(application.executableURL.path)
  });
}
JSON.stringify(identities);`
}

func desktopApplicationGuardianScript(
	application desktopApplicationIdentity,
) string {
	return desktopApplicationGuardianScriptForBundle(desktopBundleID, application)
}

func desktopApplicationGuardianScriptForBundle(
	bundleID string,
	application desktopApplicationIdentity,
) string {
	return `ObjC.import("AppKit");
ObjC.import("Foundation");
var applications = $.NSRunningApplication.runningApplicationsWithBundleIdentifier(` +
		strconv.Quote(bundleID) + `);
if (Number(ObjC.unwrap(applications.count)) !== 1) {
  throw new Error("Desktop application instance count changed");
}
var matched = null;
for (var index = 0; index < applications.count; index++) {
	var candidate = applications.objectAtIndex(index);
  if (Number(ObjC.unwrap(candidate.processIdentifier)) === ` +
		strconv.Itoa(application.ProcessID) + ` &&
		ObjC.unwrap(candidate.bundleURL.path) === ` +
		strconv.Quote(application.BundlePath) + ` &&
		ObjC.unwrap(candidate.executableURL.path) === ` +
		strconv.Quote(application.ExecutablePath) + `) {
	matched = candidate;
  }
}
if (matched === null) {
  throw new Error("Desktop application process identity changed");
}
var input = $.NSFileHandle.fileHandleWithStandardInput;
var output = $.NSFileHandle.fileHandleWithStandardOutput;
function writeLine(value) {
  var encoded = $(value + "\n").dataUsingEncoding($.NSUTF8StringEncoding);
  output.writeData(encoded);
}
writeLine("ready");
var buffered = "";
while (true) {
  var data = input.availableData;
  if (Number(ObjC.unwrap(data.length)) === 0) {
    break;
  }
  var chunk = ObjC.unwrap(
    $.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding)
  );
  if (typeof chunk !== "string") {
    throw new Error("Desktop guardian input was not UTF-8");
  }
  buffered += chunk;
  if (buffered.length > 128) {
    throw new Error("Desktop guardian input exceeded its bound");
  }
  var newline = buffered.indexOf("\n");
  while (newline >= 0) {
    var action = buffered.slice(0, newline);
    buffered = buffered.slice(newline + 1);
    if (ObjC.unwrap(matched.terminated)) {
      writeLine("gone");
    } else if (action === "terminate") {
      writeLine(ObjC.unwrap(matched.terminate) ? "accepted" : "refused");
    } else if (action === "force") {
      writeLine(ObjC.unwrap(matched.forceTerminate) ? "accepted" : "refused");
    } else {
      writeLine("refused");
    }
    newline = buffered.indexOf("\n");
  }
}`
}
