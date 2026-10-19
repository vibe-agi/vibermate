//go:build darwin

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

type packagedACPEvidence struct {
	Policy   environment.ContentRecordingPolicy
	Snapshot acpobservation.Snapshot
	State    string
}

var errPackagedACPSourceChanged = errors.New("packaged ACP source changed during private-copy inspection")

// All reads use private DB/WAL copies; SQLite never receives original paths.
// Only an explicitly detected source change is retryable by the live rendezvous.
func withPackagedACPRoles(ctx context.Context, root string, read func(string, runtimedata.StorageLayout, *sql.DB, *sql.DB) error) (result error) {
	metadataPath := filepath.Join(root, "storage-layout.json")
	metadataInfo, err := os.Lstat(metadataPath)
	if err != nil {
		return err
	}
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return err
	}
	layout, err := runtimedata.ReadStorageLayout(root)
	if err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "vibermate-acp-evidence-")
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, os.RemoveAll(temporary))
	}()
	temporary, err = filepath.EvalSymlinks(temporary)
	if err != nil {
		return err
	}
	type original struct {
		path string
		info os.FileInfo
		data []byte
	}
	originals := []original{{metadataPath, metadataInfo, metadataBytes}}
	for _, role := range []runtimedata.DatabaseRole{layout.Control, layout.Observation} {
		for _, suffix := range []string{"", "-wal", "-journal", "-shm"} {
			path := filepath.Join(root, role.RelativePath) + suffix
			info, err := os.Lstat(path)
			if suffix != "" && errors.Is(err, os.ErrNotExist) {
				originals = append(originals, original{path: path})
				continue
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("ACP role is not a regular file: %s", role.RelativePath)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				if suffix != "" && errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("%w: sidecar disappeared", errPackagedACPSourceChanged)
				}
				return err
			}
			originals = append(originals, original{path, info, data})
			if suffix != "-shm" {
				if err = os.WriteFile(filepath.Join(temporary, role.RelativePath)+suffix, data, 0600); err != nil {
					return err
				}
			}
		}
	}
	defer func() {
		for _, original := range originals {
			info, err := os.Lstat(original.path)
			if original.info == nil {
				if !errors.Is(err, os.ErrNotExist) {
					result = errors.Join(result, fmt.Errorf("%w: sidecar appeared", errPackagedACPSourceChanged))
				}
				continue
			}
			if err != nil || !os.SameFile(original.info, info) {
				result = errors.Join(result, fmt.Errorf("%w: identity changed", errPackagedACPSourceChanged))
				continue
			}
			data, err := os.ReadFile(original.path)
			if err != nil || !bytes.Equal(data, original.data) {
				result = errors.Join(result, fmt.Errorf("%w: bytes changed", errPackagedACPSourceChanged))
			}
		}
	}()
	controlPath := filepath.Join(temporary, layout.Control.RelativePath)
	observationPath := filepath.Join(temporary, layout.Observation.RelativePath)
	controlState, err := runtimepersistence.ValidateOfflineRole(ctx, controlPath, layout.Control.Identity, layout.Control.Revision, layout.ID, "", recording.InspectionStamp{})
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(layout.ID))
	if _, err = runtimepersistence.ValidateOfflineRole(ctx, observationPath, layout.Observation.Identity, layout.Observation.Revision, layout.ID, "current-"+hex.EncodeToString(digest[:16]), controlState.Stamp); err != nil {
		return err
	}
	open := func(path string) (*sql.DB, error) {
		uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
		db, err := sql.Open("sqlite", uri.String())
		if err == nil {
			db.SetMaxOpenConns(1)
		}
		return db, err
	}
	control, err := open(controlPath)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, control.Close()) }()
	observations, err := open(observationPath)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, observations.Close()) }()
	return read(temporary, layout, control, observations)
}

// Final evidence stays strict and stopped-source-only; no missing record is
// accepted merely because the live rendezvous observed zero owned slots.
func readPackagedACPEvidence(ctx context.Context, root string) (evidence []packagedACPEvidence, result error) {
	result = withPackagedACPRoles(ctx, root, func(_ string, _ runtimedata.StorageLayout, control, observations *sql.DB) error {
		rows, err := observations.QueryContext(ctx, `SELECT run_id,recording,retention_days,expires_at_unix_ms,archive_epoch,capture_generation,snapshot,revision,final FROM acp_snapshots`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value packagedACPEvidence
			var runID string
			var expiry, epoch, generation, revision int64
			var final bool
			var payload []byte
			if err = rows.Scan(&runID, &value.Policy.Mode, &value.Policy.RetentionDays, &expiry, &epoch, &generation, &payload, &revision, &final); err != nil {
				return err
			}
			var policy environment.ContentRecordingPolicy
			var registeredExpiry, registeredEpoch, registeredGeneration, currentEpoch, currentGeneration int64
			var deleted bool
			err = control.QueryRowContext(ctx, `SELECT r.state,p.recording,p.retention_days,p.expires_at_unix_ms,p.archive_epoch,p.capture_generation,s.archive_epoch,f.generation,f.deleted FROM acp_registrations p JOIN capture_runs r USING(run_id) JOIN runtime_storage_state s ON s.singleton=1 JOIN recording_capture_fences f ON f.capture_kind='managed_run' AND f.capture_id=p.run_id WHERE p.run_id=?`, runID).Scan(&value.State, &policy.Mode, &policy.RetentionDays, &registeredExpiry, &registeredEpoch, &registeredGeneration, &currentEpoch, &currentGeneration, &deleted)
			if err != nil {
				return err
			}
			if value.State != "finished" || deleted || epoch != registeredEpoch || generation != registeredGeneration || epoch != currentEpoch || generation != currentGeneration || policy != value.Policy || expiry != registeredExpiry || expiry <= time.Now().UnixMilli() {
				return errors.New("ACP snapshot differs from original finished registration")
			}
			if err = json.Unmarshal(payload, &value.Snapshot); err != nil {
				return err
			}
			if value.Snapshot.ValidatePolicy(policy) != nil || value.Snapshot.Revision != uint64(revision) || value.Snapshot.Final != final {
				return errors.New("ACP snapshot metadata contradicts payload")
			}
			evidence = append(evidence, value)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		var registered int
		if err = control.QueryRowContext(ctx, `SELECT count(*) FROM acp_registrations`).Scan(&registered); err != nil {
			return err
		}
		if registered != len(evidence) {
			return fmt.Errorf("ACP registration has no retained current snapshot: registrations=%d snapshots=%d", registered, len(evidence))
		}
		return nil
	})
	if result != nil {
		return nil, result
	}
	return evidence, nil
}
