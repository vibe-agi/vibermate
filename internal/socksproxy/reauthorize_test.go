package socksproxy

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureauthority"
)

type reauthorizeAuthority struct {
	admission captureadmission.Admission
	err       error
}

func (a *reauthorizeAuthority) Authorize(context.Context, captureadmission.ProxyCredential) (captureadmission.Admission, error) {
	if a.err != nil {
		return captureadmission.Admission{}, a.err
	}
	return a.admission, nil
}

func reauthorizeSession(t *testing.T, authority *reauthorizeAuthority, authorizedAt time.Time) *session {
	t.Helper()
	ref, err := authority.admission.CaptureReference()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	control, peer := net.Pipe()
	t.Cleanup(func() { peer.Close(); control.Close(); cancel() })
	return &session{server: &Server{options: Options{Authorizer: authority}}, control: control, ctx: ctx, cancel: cancel,
		admission: authority.admission, reference: ref, authorizedAt: authorizedAt, flows: map[Target]*udpFlow{}}
}

// A UDP association is re-checked on every packet. When storage cannot answer
// that check, an association confirmed recently keeps running; a rejection, or
// storage silent past the grace, still closes it.
func TestReauthorizeRidesOutAStorageFailureButNotARejection(t *testing.T) {
	admission, err := captureadmission.NewManual("manual.test", 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	unavailable := errors.Join(captureadmission.ErrCredentialRejected, captureauthority.ErrUnavailable, errors.New("disk I/O error"))
	for _, test := range []struct {
		name         string
		err          error
		authorizedAt time.Time
		keeps        bool
	}{
		{"storage failure within grace", unavailable, time.Now(), true},
		{"storage failure past grace", unavailable, time.Now().Add(-reauthorizeStallGrace - time.Second), false},
		{"rejection", captureadmission.ErrCredentialRejected, time.Now(), false},
		{"confirmed", nil, time.Now().Add(-time.Minute), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			x := reauthorizeSession(t, &reauthorizeAuthority{admission: admission, err: test.err}, test.authorizedAt)
			if got := x.reauthorize(); got != test.keeps {
				t.Fatalf("reauthorize kept=%v, want %v", got, test.keeps)
			}
			if closed := x.ctx.Err() != nil; closed == test.keeps {
				t.Fatalf("association closed=%v, want %v", closed, !test.keeps)
			}
		})
	}
	// A confirmed check restarts the grace.
	authority := &reauthorizeAuthority{admission: admission}
	x := reauthorizeSession(t, authority, time.Now().Add(-reauthorizeStallGrace-time.Second))
	if !x.reauthorize() {
		t.Fatal("confirmed check closed the association")
	}
	authority.err = unavailable
	if !x.reauthorize() {
		t.Fatal("storage failure right after a confirmed check closed the association")
	}
}
