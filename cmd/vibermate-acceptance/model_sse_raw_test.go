//go:build darwin

package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

type modelSnapshotSource struct {
	activities func(context.Context) (activity.Page, error)
	raw        rawevidence.Reader
}
type modelSnapshot struct {
	records []activity.Record
	raw     map[string][]rawevidence.RevealedEnvelope
}

func runtimeModelSnapshot(ctx context.Context, actual nativeModelProbeObservation) (modelSnapshot, error) {
	return awaitModelSnapshot(ctx, time.Now().Add(5*time.Second), modelSnapshotSource{activities: func(ctx context.Context) (activity.Page, error) {
		return actual.Runtime.Activities().ListExchanges(ctx, activity.PageRequest{CaptureRunID: actual.Capture.ID, Limit: 10})
	}, raw: actual.Runtime.RawEvidence()}, actual.ModelRequests)
}

func awaitModelSnapshot(ctx context.Context, deadline time.Time, source modelSnapshotSource, expected int) (modelSnapshot, error) {
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	required := []rawevidence.Layer{rawevidence.LayerClientIngress, rawevidence.LayerProviderEgress, rawevidence.LayerProviderResponse, rawevidence.LayerClientDownstream}
	for {
		if err := ctx.Err(); err != nil {
			return modelSnapshot{}, err
		}
		page, err := source.activities(ctx)
		if err != nil {
			return modelSnapshot{}, err
		}
		if len(page.Items) > expected {
			return modelSnapshot{}, errors.New("unexpected extra MODEL Activity")
		}
		ready := len(page.Items) == expected
		all := map[string][]rawevidence.EnvelopeMetadata{}
		for _, record := range page.Items {
			if record.Kind != activity.KindExchangeCompleted {
				ready = false
				continue
			}
			if _, duplicate := all[record.SubjectID]; duplicate {
				return modelSnapshot{}, errors.New("duplicate MODEL Exchange")
			}
			metadata, err := source.raw.ListExchange(ctx, record.SubjectID)
			if err != nil {
				return modelSnapshot{}, err
			}
			layers := map[rawevidence.Layer]bool{}
			for _, meta := range metadata {
				if meta.ExchangeID != record.SubjectID || meta.ScopeKind != rawevidence.ScopeManagedRun || meta.ScopeID != record.CaptureRunID || meta.EnvironmentID != record.EnvironmentID || meta.EnvironmentRevision != record.EnvironmentRevision || meta.EnvironmentDigest != record.EnvironmentDigest {
					return modelSnapshot{}, errors.New("Raw envelope ownership mismatch")
				}
				if layers[meta.Layer] {
					return modelSnapshot{}, errors.New("duplicate Raw layer")
				}
				layers[meta.Layer] = true
			}
			for _, layer := range required {
				if !layers[layer] {
					ready = false
				}
			}
			all[record.SubjectID] = metadata
		}
		if ready {
			result := modelSnapshot{records: page.Items, raw: map[string][]rawevidence.RevealedEnvelope{}}
			for _, record := range page.Items {
				for _, meta := range all[record.SubjectID] {
					if err := ctx.Err(); err != nil {
						return modelSnapshot{}, err
					}
					raw, err := source.raw.Reveal(ctx, rawevidence.RevealRequest{EnvelopeID: meta.EnvelopeID, ActorID: "synthetic-model-snapshot"})
					if err != nil {
						return modelSnapshot{}, err
					}
					if !reflect.DeepEqual(raw.Metadata, meta) {
						return modelSnapshot{}, errors.New("Revealed Raw metadata changed")
					}
					if meta.PayloadState != rawevidence.PayloadCaptured && meta.PayloadState != rawevidence.PayloadTruncated {
						return modelSnapshot{}, errors.New("required Raw payload unavailable")
					}
					if meta.BodyBytes < int64(len(raw.Payload.Body)) || meta.PayloadState == rawevidence.PayloadCaptured && meta.BodyBytes != int64(len(raw.Payload.Body)) {
						return modelSnapshot{}, errors.New("Raw retained/total byte projection differs")
					}
					if meta.DigestScope == rawevidence.DigestFull && meta.BodyBytes == int64(len(raw.Payload.Body)) && sha256.Sum256(raw.Payload.Body) != meta.BodySHA256 {
						return modelSnapshot{}, errors.New("Raw full digest mismatch")
					}
					if meta.PayloadState == rawevidence.PayloadTruncated && meta.PayloadReason == "" {
						return modelSnapshot{}, errors.New("truncated Raw lacks reason")
					}
					for _, frame := range raw.Payload.Frames {
						if frame.Offset < 0 || frame.Length <= 0 || frame.Offset > int64(len(raw.Payload.Body)) || frame.Length > int64(len(raw.Payload.Body))-frame.Offset {
							return modelSnapshot{}, errors.New("Raw frame outside retained prefix")
						}
					}
					if meta.Layer == rawevidence.LayerClientDownstream && (len(raw.Payload.Body) == 0 || len(raw.Payload.Frames) == 0) {
						return modelSnapshot{}, errors.New("client downstream body/frames missing")
					}
					result.raw[record.SubjectID] = append(result.raw[record.SubjectID], raw)
				}
			}
			if err := ctx.Err(); err != nil {
				return modelSnapshot{}, err
			}
			return result, nil
		}
		select {
		case <-ctx.Done():
			return modelSnapshot{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type modelSnapshotFixture struct {
	metadata map[string][]rawevidence.EnvelopeMetadata
	payload  map[string]rawevidence.RevealedEnvelope
	reveals  int
	list     func(context.Context, string) ([]rawevidence.EnvelopeMetadata, error)
}

func (f *modelSnapshotFixture) ListExchange(ctx context.Context, id string) ([]rawevidence.EnvelopeMetadata, error) {
	if f.list != nil {
		return f.list(ctx, id)
	}
	return f.metadata[id], nil
}
func (f *modelSnapshotFixture) Reveal(_ context.Context, r rawevidence.RevealRequest) (rawevidence.RevealedEnvelope, error) {
	f.reveals++
	return f.payload[r.EnvelopeID], nil
}
func (*modelSnapshotFixture) Recovery() rawevidence.Recovery { return rawevidence.Recovery{} }

func TestModelSnapshotRejectsMissingClientDownstream(t *testing.T) {
	source, reader := completeModelSnapshotFixture(1)
	reader.metadata["exchange0"] = reader.metadata["exchange0"][:3]
	_, err := awaitModelSnapshot(context.Background(), time.Now().Add(50*time.Millisecond), source, 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("terminal Activity with only three Raw layers incorrectly satisfied completed snapshot")
	}
	if reader.reveals != 0 {
		t.Error("incomplete metadata triggered premature Reveal audits")
	}
}

func completeModelSnapshotFixture(count int) (modelSnapshotSource, *modelSnapshotFixture) {
	reader := &modelSnapshotFixture{metadata: map[string][]rawevidence.EnvelopeMetadata{}, payload: map[string]rawevidence.RevealedEnvelope{}}
	records := []activity.Record{}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("exchange%d", i)
		record := activity.Record{SubjectID: id, Kind: activity.KindExchangeCompleted, Status: activity.StatusSucceeded, CaptureRunID: "capture", EnvironmentID: "environment", EnvironmentRevision: 1, EnvironmentDigest: "digest"}
		records = append(records, record)
		for _, layer := range []rawevidence.Layer{rawevidence.LayerClientIngress, rawevidence.LayerProviderEgress, rawevidence.LayerProviderResponse, rawevidence.LayerClientDownstream} {
			body := []byte("synthetic payload")
			meta := rawevidence.EnvelopeMetadata{EnvelopeID: id + "." + string(layer), ExchangeID: id, Layer: layer, ScopeKind: rawevidence.ScopeManagedRun, ScopeID: record.CaptureRunID, EnvironmentID: record.EnvironmentID, EnvironmentRevision: 1, EnvironmentDigest: record.EnvironmentDigest, PayloadState: rawevidence.PayloadCaptured, DigestScope: rawevidence.DigestFull, BodyBytes: int64(len(body)), BodySHA256: sha256.Sum256(body)}
			raw := rawevidence.RevealedEnvelope{Metadata: meta, Payload: rawevidence.Payload{Body: body}}
			if layer == rawevidence.LayerClientDownstream {
				raw.Payload.Frames = []rawevidence.Frame{{Kind: rawevidence.FrameData, Length: int64(len(body))}}
			}
			reader.metadata[id] = append(reader.metadata[id], meta)
			reader.payload[meta.EnvelopeID] = raw
		}
	}
	return modelSnapshotSource{activities: func(context.Context) (activity.Page, error) { return activity.Page{Items: records}, nil }, raw: reader}, reader
}

func TestModelSnapshotWaitsWithoutRenewingOneDeadline(t *testing.T) {
	for _, scenario := range []string{"late_ready", "shared_deadline", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			count := 1
			if scenario == "shared_deadline" {
				count = 2
			}
			source, reader := completeModelSnapshotFixture(count)
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			deadline := time.Now().Add(80 * time.Millisecond)
			start := time.Now()
			calls := 0
			contextMatched := true
			baseActivities := source.activities
			source.activities = func(ctx context.Context) (activity.Page, error) {
				d, ok := ctx.Deadline()
				contextMatched = contextMatched && ok && d.Equal(deadline)
				return baseActivities(ctx)
			}
			reader.list = func(ctx context.Context, id string) ([]rawevidence.EnvelopeMetadata, error) {
				calls++
				d, ok := ctx.Deadline()
				contextMatched = contextMatched && ok && d.Equal(deadline)
				ready := time.Since(start) >= 20*time.Millisecond
				if scenario == "shared_deadline" && id == "exchange1" {
					ready = time.Since(start) >= 120*time.Millisecond
				}
				if !ready {
					if reader.reveals != 0 {
						t.Error("Reveal occurred before all required metadata")
					}
					return reader.metadata[id][:3], nil
				}
				return reader.metadata[id], nil
			}
			if scenario == "canceled" {
				cancel()
			}
			_, err := awaitModelSnapshot(parent, deadline, source, count)
			if scenario == "late_ready" {
				if err != nil || reader.reveals != 4 || calls < 2 || !contextMatched {
					t.Fatalf("late complete metadata not observed once: %v", err)
				}
			} else {
				want := context.DeadlineExceeded
				if scenario == "canceled" {
					want = context.Canceled
				}
				if !errors.Is(err, want) || reader.reveals != 0 || time.Since(start) > 160*time.Millisecond {
					t.Fatal("shared observation budget renewed or incomplete Raw accepted")
				}
			}
		})
	}
}

func TestModelSnapshotRejectsCorruptOrEmptyPublishedEvidence(t *testing.T) {
	for _, scenario := range []string{"wrong_scope", "wrong_environment", "duplicate_layer", "revealed_metadata_mismatch", "empty_frames", "empty_body", "frame_overflow", "negative_offset", "bad_hash", "unavailable", "prefix_preserved"} {
		t.Run(scenario, func(t *testing.T) {
			source, reader := completeModelSnapshotFixture(1)
			meta := reader.metadata["exchange0"][3]
			raw := reader.payload[meta.EnvelopeID]
			switch scenario {
			case "wrong_scope":
				meta.ScopeID = "wrong"
			case "wrong_environment":
				meta.EnvironmentDigest = "wrong"
			case "duplicate_layer":
				reader.metadata["exchange0"] = append(reader.metadata["exchange0"], meta)
			case "revealed_metadata_mismatch":
				raw.Metadata.ExchangeID = "wrong"
			case "empty_frames":
				raw.Payload.Frames = nil
			case "empty_body":
				raw.Payload.Body = nil
				meta.BodyBytes = 0
				meta.BodySHA256 = sha256.Sum256(nil)
			case "frame_overflow":
				raw.Payload.Frames[0].Length++
			case "negative_offset":
				raw.Payload.Frames[0].Offset = -1
			case "bad_hash":
				meta.BodySHA256 = [32]byte{1}
			case "unavailable":
				meta.PayloadState = rawevidence.PayloadUnavailable
			case "prefix_preserved":
				meta = reader.metadata["exchange0"][2]
				raw = reader.payload[meta.EnvelopeID]
				meta.PayloadState = rawevidence.PayloadTruncated
				meta.PayloadReason = "response_canceled"
				meta.DigestScope = rawevidence.DigestObservedPrefix
				meta.BodyBytes += 10
			}
			index := 3
			if scenario == "prefix_preserved" {
				index = 2
			}
			reader.metadata["exchange0"][index] = meta
			if scenario != "revealed_metadata_mismatch" {
				raw.Metadata = meta
			}
			reader.payload[meta.EnvelopeID] = raw
			result, err := awaitModelSnapshot(context.Background(), time.Now().Add(100*time.Millisecond), source, 1)
			if scenario == "prefix_preserved" {
				if err != nil || result.raw["exchange0"][2].Metadata.PayloadState != rawevidence.PayloadTruncated || result.raw["exchange0"][2].Metadata.BodyBytes != meta.BodyBytes {
					t.Fatal("truthful canceled prefix projection lost")
				}
			} else if err == nil {
				t.Fatal("published empty/corrupt Raw satisfied readiness")
			}
		})
	}
}
