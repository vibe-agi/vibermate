package nativemcp

import (
	"bytes"
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/environment"
	"io"
	"net/http/httptest"
	"testing"
)

type boundaryReader struct {
	prefix *bytes.Reader
	steps  []error
	reads  int
}

func (r *boundaryReader) Read(data []byte) (int, error) {
	if r.prefix.Len() > 0 {
		return r.prefix.Read(data)
	}
	r.reads++
	if len(r.steps) > 0 {
		err := r.steps[0]
		r.steps = r.steps[1:]
		return 0, err
	}
	return 0, nil
}
func TestNativeIncomingExactMaximumDoesNotInventOverflow(t *testing.T) {
	maximum := DefaultLimits().RequestBytes
	for _, tc := range []struct {
		name      string
		tail      []error
		want      error
		wantReads int
	}{
		{"unexpected_eof", []error{io.ErrUnexpectedEOF}, io.ErrUnexpectedEOF, 1},
		{"deadline", []error{context.DeadlineExceeded}, context.DeadlineExceeded, 1},
		{"zero_then_eof", []error{nil, io.EOF}, nil, 2},
		{"no_progress", nil, io.ErrNoProgress, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			reader := &boundaryReader{prefix: bytes.NewReader(bytes.Repeat([]byte{' '}, int(maximum))), steps: tc.tail}
			data, release, err := f.executor.(*executor).readBounded(reader, maximum)
			defer release()
			if !errors.Is(err, tc.want) || errors.Is(err, errIncomingBodyTooLarge) {
				t.Fatalf("exact maximum read error=%v want=%v", err, tc.want)
			}
			if tc.want == nil && int64(len(data)) != maximum {
				t.Fatal("exact maximum was truncated")
			}
			if reader.reads != tc.wantReads {
				t.Fatalf("tail reads%d want%d", reader.reads, tc.wantReads)
			}
		})
	}
	t.Run("maximum_plus_one", func(t *testing.T) {
		f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
		_, release, err := f.executor.(*executor).readBounded(bytes.NewReader(bytes.Repeat([]byte{' '}, int(maximum+1))), maximum)
		defer release()
		if !errors.Is(err, errIncomingBodyTooLarge) {
			t.Fatalf("observed extra byte %v", err)
		}
	})
}

func TestNativeIncomingExactMaximumReadFailureIsNotHTTP413(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	captured := f.captured(t, "POST", "", "")
	captured.request.Body = io.NopCloser(&boundaryReader{prefix: bytes.NewReader(bytes.Repeat([]byte{' '}, int(DefaultLimits().RequestBytes))), steps: []error{io.ErrUnexpectedEOF}})
	recorder := httptest.NewRecorder()
	terminal := f.executor.Serve(context.Background(), captured, recorder)
	if recorder.Code == 413 || terminal.Reason == ReasonBudgetExceeded || f.transport.sends() != 0 {
		t.Fatalf("exact maximum error status%d terminal%+v sends%d", recorder.Code, terminal, f.transport.sends())
	}
}
