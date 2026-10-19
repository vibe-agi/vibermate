//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

type packagedACPDiscovery struct {
	source   runlauncher.Discovery
	expected localdiscovery.Session
}

func (d packagedACPDiscovery) Load() (localdiscovery.Session, error) {
	current, err := d.source.Load()
	if err != nil {
		return localdiscovery.Session{}, err
	}
	if current.Schema != d.expected.Schema || current.InstanceID != d.expected.InstanceID || current.ProcessID != d.expected.ProcessID || current.BaseURL != d.expected.BaseURL || current.ControlCredential != d.expected.ControlCredential {
		return localdiscovery.Session{}, errors.New("packaged ACP CLI generation changed")
	}
	return current, nil
}

// Positive retained-history fixtures inspect only the generation-bound CLI
// status capability. Required peer/ACP commands are never passed or retried.
func waitPackagedACPRecording(ctx context.Context, discovery runlauncher.Discovery, observe ...func(productruntime.RecordingStatus)) error {
	read, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		value, err := runlauncher.InspectLocal(read, discovery, time.Second)
		if err != nil {
			return err
		}
		state := value.Recording
		if value.Host != "desktop" || value.Storage != "healthy" || value.StorageFailure != nil || value.RecordingFailure != nil || state.Unconfirmed != 0 {
			return fmt.Errorf("packaged ACP recording unavailable: host=%s storage=%s state=%s dropped=%d unconfirmed=%d storageFailure=%+v recordingFailure=%+v", value.Host, value.Storage, state.State, state.Dropped, state.Unconfirmed, value.StorageFailure, value.RecordingFailure)
		}
		switch state.State {
		case productruntime.RecordingStarting, productruntime.RecordingDegraded, productruntime.RecordingAvailable:
		default:
			return errors.New("packaged ACP recording state is unsupported")
		}
		if state.ReservedRecords < 0 || state.ReservedBytes < 0 {
			return errors.New("packaged ACP recording ownership is invalid")
		}
		// Known lifetime drops can coexist with complete final ACP evidence.
		// Scope readiness and the strict stopped reader are separate proofs.
		settledState := state.State == productruntime.RecordingAvailable || (state.State == productruntime.RecordingDegraded && state.Dropped > 0)
		if value.Ready && settledState && state.ReservedRecords == 0 && state.ReservedBytes == 0 && !state.CleanupPending {
			if len(observe) != 0 {
				observe[0](state)
			}
			return nil
		}
		select {
		case <-read.Done():
			return read.Err()
		case <-tick.C:
		}
	}
}
