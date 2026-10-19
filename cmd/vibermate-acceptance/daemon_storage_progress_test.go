package main

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
)

func TestDecodeDescriptorStorageProgressBeforeTerminal(t *testing.T) {
	for _, phase := range []desktopbootstrap.ProgressPhase{desktopbootstrap.ProgressStoragePreparing, desktopbootstrap.ProgressStorageUpgrade} {
		for _, failure := range []bool{false, true} {
			name := string(phase) + "/descriptor"
			if failure {
				name = string(phase) + "/failure"
			}
			t.Run(name, func(t *testing.T) {
				frames := diagnosticTestFrames(t)
				firstEnd := bytes.IndexByte(frames, '\n') + 1
				terminal := frames[firstEnd:]
				var want desktopbootstrap.Descriptor
				if err := json.Unmarshal(terminal, &want); err != nil {
					t.Fatal(err)
				}
				if failure {
					encoded, err := json.Marshal(desktopbootstrap.StartupFailure(desktopbootstrap.FailureStorageUpgradeIncomplete))
					if err != nil {
						t.Fatal(err)
					}
					terminal = append(encoded, '\n')
					want = desktopbootstrap.Descriptor{}
				}
				progress, err := json.Marshal(desktopbootstrap.Progress{Schema: desktopbootstrap.ProgressSchema, Phase: phase})
				if err != nil {
					t.Fatal(err)
				}
				payload := append(bytes.Clone(frames[:firstEnd]), append(progress, '\n')...)
				payload = append(payload, terminal...)
				reader, writer := io.Pipe()
				t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
				type result struct {
					descriptor desktopbootstrap.Descriptor
					err        error
					observed   []int
				}
				decoded := make(chan result, 1)
				go func() {
					var observed []int
					descriptor, err := decodeDescriptorObserved(reader, func(frame int) { observed = append(observed, frame) })
					decoded <- result{descriptor, err, observed}
				}()
				if _, err := writer.Write(payload); err != nil {
					t.Fatal(err)
				}
				select {
				case got := <-decoded:
					if failure {
						if got.err == nil || !strings.Contains(got.err.Error(), "reason=storage_upgrade_incomplete") {
							t.Fatalf("closed startup failure lost after storage progress: %v", got.err)
						}
					} else if got.err != nil {
						t.Fatal(got.err)
					}
					if !reflect.DeepEqual(got.descriptor, want) || !slices.Equal(got.observed, []int{1, 2}) {
						t.Fatalf("terminal or raw-frame observations changed: descriptor=%+v observed=%v", got.descriptor, got.observed)
					}
				case <-time.After(time.Second):
					t.Fatal("decoder waited for EOF after complete terminal frame")
				}
			})
		}
	}
}

func TestDecodeDescriptorStorageProgressKeepsClosedBounds(t *testing.T) {
	frames := diagnosticTestFrames(t)
	firstEnd := bytes.IndexByte(frames, '\n') + 1
	initial, descriptor := frames[:firstEnd], frames[firstEnd:]
	encode := func(value any) []byte {
		t.Helper()
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return append(payload, '\n')
	}
	preparing := encode(desktopbootstrap.Progress{Schema: desktopbootstrap.ProgressSchema, Phase: desktopbootstrap.ProgressStoragePreparing})
	upgrade := encode(desktopbootstrap.Progress{Schema: desktopbootstrap.ProgressSchema, Phase: desktopbootstrap.ProgressStorageUpgrade})
	var oversized desktopbootstrap.Descriptor
	if err := json.Unmarshal(descriptor, &oversized); err != nil {
		t.Fatal(err)
	}
	oversized.InstanceID = strings.Repeat("x", bootstrapLimit)
	var unexpectedDescriptor map[string]any
	if err := json.Unmarshal(descriptor, &unexpectedDescriptor); err != nil {
		t.Fatal(err)
	}
	unexpectedDescriptor["phase"] = "storage_preparing"
	for _, test := range []struct {
		name   string
		middle []byte
		last   []byte
	}{
		{"unknown phase", encode(desktopbootstrap.Progress{Schema: desktopbootstrap.ProgressSchema, Phase: "unknown"}), descriptor},
		{"repeated starting", initial, descriptor},
		{"repeated storage", append(bytes.Clone(preparing), preparing...), descriptor},
		{"two storage phases", append(bytes.Clone(preparing), upgrade...), descriptor},
		{"unknown progress field", []byte(`{"schema":"vibermate-daemon-progress-v1","phase":"storage_preparing","bootstrapNonce":"not permitted"}` + "\n"), descriptor},
		{"trailing progress JSON", append(bytes.Clone(bytes.TrimSuffix(preparing, []byte{'\n'})), []byte("{}\n")...), descriptor},
		{"unknown descriptor field", preparing, encode(unexpectedDescriptor)},
		{"missing terminal", preparing, nil},
		{"partial terminal", preparing, bytes.TrimSuffix(descriptor, []byte{'\n'})},
		{"oversize third frame", preparing, encode(oversized)},
		{"unknown failure reason", preparing, encode(desktopbootstrap.StartupFailure("unknown"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := append(bytes.Clone(initial), test.middle...)
			payload = append(payload, test.last...)
			got, err := decodeDescriptor(bytes.NewReader(payload))
			if err == nil || !reflect.DeepEqual(got, desktopbootstrap.Descriptor{}) {
				t.Fatalf("invalid bounded startup yielded authority: descriptor=%+v err=%v", got, err)
			}
		})
	}
}
