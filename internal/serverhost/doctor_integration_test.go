package serverhost_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

type doctorForbiddenEgress struct{ calls atomic.Int32 }

func (network *doctorForbiddenEgress) DialContext(context.Context, string, string) (net.Conn, error) {
	network.calls.Add(1)
	return nil, errors.New("doctor must not dial upstream")
}
func (network *doctorForbiddenEgress) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	network.calls.Add(1)
	return nil, errors.New("doctor must not resolve upstream")
}

func TestDoctorEntryUsesProductionHTTPAndTLSWithoutCaptureOrUpstream(t *testing.T) {
	for _, mode := range []serverhost.TransportMode{serverhost.TransportHTTP, serverhost.TransportPrivateCATLS} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			options := serverOptions(t, root)
			options.Transport = serverhost.TransportOptions{Mode: mode}
			network := &doctorForbiddenEgress{}
			options.Runtime.NetworkOptions = egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network}
			host, err := serverhost.Start(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownServer(t, host)
			scheme := "http"
			if mode != serverhost.TransportHTTP {
				scheme = "https"
			}
			remote := runlauncher.RemoteConfig{Target: mustServerTarget(t, scheme+"://"+host.Status().ListenAddress), StateDirectory: filepath.Join(root, "client"), DisplayName: "doctor-fixture", Clock: productruntime.SystemClock{}, Random: rand.Reader}
			loginRemoteTestUser(t, host, remote)
			report, err := runlauncher.DiagnoseRemote(t.Context(), remote.Target, remote.StateDirectory, remote.Clock, environment.SystemTransparentID)
			if err != nil || !report.Control.Checked || report.Control.Reason != "" || !report.Entry.Checked || report.Entry.Reason != "" || !report.Environment.Checked || report.Environment.Reason != "" {
				t.Fatalf("diagnosis=%+v err=%v", report, err)
			}
			page, err := host.Runtime().CaptureRunReader().ListRuns(t.Context(), capturerun.PageRequest{})
			if err != nil || len(page.Items) != 0 {
				t.Fatalf("doctor created Capture: %v %v", page, err)
			}
			if network.calls.Load() != 0 {
				t.Fatalf("doctor made %d upstream operations", network.calls.Load())
			}
		})
	}
}
