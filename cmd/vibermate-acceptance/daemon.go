package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/loopbackclient"
)

const (
	bootstrapLimit  = 16 << 10
	capabilityBytes = 32
)

type daemonGeneration struct {
	command    *exec.Cmd
	descriptor desktopbootstrap.Descriptor
	control    *controlClient
	done       chan error
	stderr     *boundedBuffer
}

func startDaemon(
	ctx context.Context,
	config config,
	appCacheDirectory string,
	dataDirectory string,
) (*daemonGeneration, error) {
	if ctx == nil {
		return nil, errors.New("daemon startup context is nil")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create daemon bootstrap pipe: %w", err)
	}
	defer reader.Close()
	command := exec.Command(
		config.daemonPath,
		daemonArguments(appCacheDirectory, dataDirectory)...,
	)
	command.ExtraFiles = []*os.File{writer}
	stderr := newBoundedBuffer(64 << 10)
	command.Stderr = stderr
	command.Stdout = io.Discard
	if err := command.Start(); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("start packaged daemon: %w", err)
	}
	_ = writer.Close()
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	descriptorResult := make(chan struct {
		descriptor desktopbootstrap.Descriptor
		err        error
	}, 1)
	go func() {
		descriptor, decodeErr := decodeDescriptor(reader)
		descriptorResult <- struct {
			descriptor desktopbootstrap.Descriptor
			err        error
		}{descriptor: descriptor, err: decodeErr}
	}()
	var descriptor desktopbootstrap.Descriptor
	select {
	case result := <-descriptorResult:
		if result.err != nil {
			_ = command.Process.Kill()
			<-done
			return nil, result.err
		}
		descriptor = result.descriptor
	case waitErr := <-done:
		return nil, fmt.Errorf(
			"packaged daemon exited before bootstrap: %w",
			normalizeWaitError(waitErr),
		)
	case <-ctx.Done():
		_ = command.Process.Kill()
		<-done
		return nil, ctx.Err()
	case <-time.After(20 * time.Second):
		_ = command.Process.Kill()
		<-done
		return nil, errors.New("packaged daemon bootstrap deadline exceeded")
	}
	control, err := exchangeControlSession(ctx, descriptor)
	if err != nil {
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
		return nil, err
	}
	return &daemonGeneration{
		command:    command,
		descriptor: descriptor,
		control:    control,
		done:       done,
		stderr:     stderr,
	}, nil
}

func daemonArguments(
	appCacheDirectory string,
	dataDirectory string,
) []string {
	return []string{
		"--app-cache-dir=" + appCacheDirectory,
		"--data-dir=" + dataDirectory,
		"--webview-origin=tauri://localhost",
		"--bootstrap-fd=3",
	}
}

func decodeDescriptor(reader io.Reader) (desktopbootstrap.Descriptor, error) {
	buffered := bufio.NewReader(io.LimitReader(reader, bootstrapLimit+1))
	payload, err := buffered.ReadBytes('\n')
	if err != nil {
		return desktopbootstrap.Descriptor{}, fmt.Errorf(
			"read daemon bootstrap descriptor: %w",
			err,
		)
	}
	if len(payload) > bootstrapLimit {
		return desktopbootstrap.Descriptor{}, errors.New(
			"daemon bootstrap descriptor exceeds its size limit",
		)
	}
	var descriptor desktopbootstrap.Descriptor
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return desktopbootstrap.Descriptor{}, fmt.Errorf(
			"decode daemon bootstrap descriptor: %w",
			err,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return desktopbootstrap.Descriptor{}, errors.New(
			"daemon bootstrap descriptor contains trailing data",
		)
	}
	if descriptor.Schema != desktopbootstrap.DescriptorSchema ||
		descriptor.InstanceID == "" ||
		descriptor.ProcessID <= 0 ||
		descriptor.BaseURL == "" ||
		len(descriptor.APIVersions) != 1 ||
		descriptor.APIVersions[0] != "v1" ||
		descriptor.EventVersions == nil ||
		len(descriptor.EventVersions) != 0 ||
		!validCapability(descriptor.BootstrapNonce) {
		return desktopbootstrap.Descriptor{}, errors.New(
			"daemon bootstrap descriptor is incomplete",
		)
	}
	return descriptor, nil
}

func (generation *daemonGeneration) stopGracefully(
	ctx context.Context,
) error {
	if generation == nil || generation.command == nil {
		return nil
	}
	if err := generation.command.Process.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("signal packaged daemon: %w", err)
	}
	select {
	case err := <-generation.done:
		return normalizeWaitError(err)
	case <-ctx.Done():
		_ = generation.command.Process.Kill()
		<-generation.done
		return ctx.Err()
	}
}

func (generation *daemonGeneration) kill(ctx context.Context) error {
	if generation == nil || generation.command == nil {
		return nil
	}
	if err := generation.command.Process.Kill(); err != nil {
		return fmt.Errorf("kill packaged daemon: %w", err)
	}
	select {
	case err := <-generation.done:
		if err == nil {
			return errors.New("killed packaged daemon exited successfully")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func normalizeWaitError(err error) error {
	if err == nil {
		return nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return fmt.Errorf("process exit code %d", exitError.ExitCode())
	}
	return err
}

func exchangeControlSession(
	ctx context.Context,
	descriptor desktopbootstrap.Descriptor,
) (*controlClient, error) {
	client, err := loopbackclient.New(descriptor.BaseURL, 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		descriptor.BaseURL+"/api/v1/auth/sessions",
		nil,
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set(
		"Authorization",
		"Bootstrap "+descriptor.BootstrapNonce,
	)
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("exchange Desktop bootstrap session: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated ||
		response.Header.Get("Cache-Control") != "no-store" {
		return nil, errors.New("Desktop bootstrap session was rejected")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, bootstrapLimit+1))
	if err != nil || len(payload) > bootstrapLimit {
		return nil, errors.New("Desktop bootstrap session response is invalid")
	}
	var session desktopbootstrap.Session
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&session); err != nil {
		return nil, errors.New("Desktop bootstrap session response is invalid")
	}
	if session.Schema != desktopbootstrap.SessionSchema ||
		session.InstanceID != descriptor.InstanceID ||
		session.BaseURL != descriptor.BaseURL ||
		!session.ExpiresAt.After(time.Now().UTC()) ||
		!validCapability(session.ReadToken) ||
		!validCapability(session.WriteToken) ||
		session.ReadToken == session.WriteToken {
		return nil, errors.New("Desktop control session is incomplete")
	}
	return newControlClient(session)
}

func validCapability(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == capabilityBytes
}

func discoveryRemoved(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return true, nil
	case err != nil:
		return false, err
	default:
		return false, nil
	}
}

func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("acceptance directory is not private")
	}
	return nil
}

func cleanTemporaryData(path string) {
	if path == "" ||
		!filepath.IsAbs(path) ||
		!strings.HasPrefix(filepath.Base(path), "vibermate-assembly-") {
		return
	}
	_ = os.RemoveAll(path)
}
