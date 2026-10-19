package runtimepersistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestContentInspectionCancellationAfterSelection(t *testing.T) {
	for _, test := range []struct {
		name          string
		batch, caller bool
	}{{"owner_single", false, false}, {"owner_batch", true, false}, {"caller_single", false, true}, {"caller_batch", true, true}} {
		t.Run(test.name, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			record := transcriptContentRecordFixture(t, "canceled-content", time.Now().UTC().Truncate(time.Millisecond), exchangecontent.ParentRef{CaptureRunID: f.scope.Capture.ID}, []transcriptMessage{{role: protocolcore.RoleUser, text: "request"}}, "response")
			limits, err := exchangecontent.DefaultSourceLimits()
			if err != nil {
				t.Fatal(err)
			}
			source, err := exchangecontent.SourceFromRecordWithin(limits, record)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.current.ExchangeContentRepository().(exchangecontent.ScopedSourceRepository).PutScopedSource(t.Context(), f.scope, source); err != nil {
				t.Fatal(err)
			}
			reader, err := NewContentInspectionReader(f.reader.catalog, f.control, limits)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := reader.Get(t.Context(), record.ExchangeID); err != nil || got.ExchangeID != record.ExchangeID {
				t.Fatal("real scoped content was not readable", err)
			}
			caller, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered := false
			read := func(s *contentInspectionRead) (string, error) {
				entered = true
				if test.caller {
					cancel()
					select {
					case <-s.lease.ctx.Done():
					case <-time.After(time.Second):
						t.Fatal("caller cancellation did not reach owned SQL")
					}
				} else {
					closeInspectionOwnerDuringRead(t, f.reader.catalog, caller, s.lease.ctx)
				}
				var value string
				err := s.tx.QueryRowContext(s.lease.ctx, `SELECT 'not disclosed'`).Scan(&value)
				return value, err
			}
			if test.batch {
				values, expiry, readErr := readInspectedContentBatch(reader, caller, []string{record.ExchangeID}, func(s *contentInspectionRead, _ locatedContent) (string, bool, error) {
					value, err := read(s)
					return value, true, err
				})
				err = readErr
				if values != nil || !expiry.IsZero() {
					t.Fatal("canceled content batch returned partial data", values, expiry)
				}
			} else {
				value, readErr := readInspectedContent(reader, caller, record.ExchangeID, "", func(s *contentInspectionRead, _ locatedContent, _ *exchangeContentRepository, _ string) (string, error) {
					return read(s)
				})
				err = readErr
				if value != "" {
					t.Fatal("canceled content read returned data", value)
				}
			}
			if !entered || !errors.Is(err, context.Canceled) || errors.Is(err, ErrInspectionUnavailable) == test.caller {
				t.Fatalf("generic read lost owner/caller classification: entered=%t err=%v caller=%v", entered, err, caller.Err())
			}
			catalog := f.reader.catalog
			catalog.mu.Lock()
			active, slots := catalog.active, len(catalog.slots)
			catalog.mu.Unlock()
			if active != 0 || slots != 0 {
				t.Fatalf("canceled helper retained ownership: active=%d slots=%d", active, slots)
			}
			if test.caller {
				if catalog.owner.Err() != nil || !errors.Is(caller.Err(), context.Canceled) {
					t.Fatal("caller cancellation closed the catalog")
				}
				if _, err := reader.Get(t.Context(), record.ExchangeID); err != nil {
					t.Fatal("caller cancellation poisoned the next read", err)
				}
			} else if caller.Err() != nil {
				t.Fatal("owner closure canceled original caller", caller.Err())
			}
		})
	}
}
