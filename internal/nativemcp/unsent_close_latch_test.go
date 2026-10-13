package nativemcp

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func TestNativeSessionCloseFailureLatchAndMemoryDrain(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful_close", true: "failed_close"}[fail], func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			e := f.executor.(*executor)
			e.mu.Lock()
			owner := e.scopes[f.capture.Key()]
			var s *session
			for _, found := range owner.sessions {
				s = found
			}
			e.mu.Unlock()
			s.mu.Lock()
			retained := s.retained
			s.mu.Unlock()
			e.budget.mu.Lock()
			before := e.budget.used
			e.budget.mu.Unlock()
			if fail {
				if err := f.store.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			err := e.closeSession(context.Background(), s)
			if fail && !errors.Is(err, nativetool.ErrUnavailable) || !fail && err != nil {
				t.Fatalf("actual first close=%v", err)
			}
			e.mu.Lock()
			_, linked := owner.sessions[s.id]
			keptOwner := e.scopes[f.capture.Key()] == owner
			fenced := owner.revoked && owner.context.Err() != nil
			e.mu.Unlock()
			s.mu.Lock()
			closed, latched, left := s.closed, s.closeFailed, s.retained
			s.mu.Unlock()
			e.budget.mu.Lock()
			after := e.budget.used
			e.budget.mu.Unlock()
			if linked || !keptOwner || !closed || latched != fail || fenced != fail || left != 0 || after != before-retained {
				t.Fatal("close lost failure/owner obligation or failed exact memory drain")
			}
			for i := 0; i < 2; i++ {
				err = e.closeSession(context.Background(), s)
				if fail && !errors.Is(err, nativetool.ErrUnavailable) || !fail && err != nil {
					t.Fatalf("repeat close forgot original disposition: %v", err)
				}
			}
			e.budget.mu.Lock()
			repeated := e.budget.used
			e.budget.mu.Unlock()
			if repeated != after {
				t.Fatal("repeat close released memory twice")
			}
		})
	}
}
