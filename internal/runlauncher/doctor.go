package runlauncher

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/loopbackclient"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servertransport"
)

var ErrProxyEntry = errors.New("Runtime Server proxy entry check failed")

// ProbeRemoteEntry checks only the credential-free CONNECT rejection, through
// the same relay and stream transport as a captured client. It cannot authorize
// forwarding and sends no request to an upstream service.
func ProbeRemoteEntry(ctx context.Context, target serverconnection.Target, directory string, clock RemoteClock, timeout time.Duration) error {
	transport, err := servertransport.Open(servertransport.Options{Target: target, TrustDirectory: filepath.Join(directory, "trust"), Clock: clock, Timeout: timeout, ReadOnlyTrust: true})
	if err != nil {
		return errors.Join(ErrProxyEntry, err)
	}
	defer transport.Close()
	return probeEntry(ctx, transport, timeout)
}

type diagnosticDialer struct {
	dialer serverStreamDialer
	ctx    context.Context
	mu     sync.Mutex
	err    error
}

func (d *diagnosticDialer) Dial(relayContext context.Context) (net.Conn, error) {
	ctx, cancel := context.WithCancel(d.ctx)
	joined := make(chan struct{})
	stop := context.AfterFunc(relayContext, func() { defer close(joined); cancel() })
	defer func() {
		if !stop() {
			<-joined
		}
		cancel()
	}()
	connection, err := d.dialer.Dial(ctx)
	d.mu.Lock()
	d.err = err
	d.mu.Unlock()
	return connection, err
}
func (d *diagnosticDialer) failure() error { d.mu.Lock(); defer d.mu.Unlock(); return d.err }

func probeEntry(ctx context.Context, dialer serverStreamDialer, timeout time.Duration) error {
	check, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	observed := &diagnosticDialer{dialer: dialer, ctx: check}
	relay, err := startLocalServerRelay(observed)
	if err != nil {
		return errors.Join(ErrProxyEntry, err)
	}
	defer relay.Close()
	local, err := loopbackclient.New(relay.Origin(), timeout)
	if err != nil {
		return errors.Join(ErrProxyEntry, err)
	}
	defer local.Close()
	connection, err := local.Dial(check)
	if err != nil {
		return errors.Join(ErrProxyEntry, err)
	}
	defer connection.Close()
	// Closing the local socket releases a blocked read; relay.Close then joins
	// the remote dial and both copy directions before returning.
	joined := make(chan struct{})
	stop := context.AfterFunc(check, func() { defer close(joined); _ = connection.Close() })
	defer func() {
		if !stop() {
			<-joined
		}
	}()
	deadline, _ := check.Deadline()
	_ = connection.SetDeadline(deadline)
	_, err = io.WriteString(connection, "CONNECT vibermate-doctor.invalid:443 HTTP/1.1\r\nHost: vibermate-doctor.invalid:443\r\nConnection: close\r\n\r\n")
	if err != nil {
		return errors.Join(ErrProxyEntry, err)
	}
	// Read the header within a hard byte limit before asking net/http to parse
	// it; a hostile peer cannot make ReadResponse allocate unbounded headers.
	reader := bufio.NewReader(connection)
	var header bytes.Buffer
	for header.Len() <= 16<<10 {
		line, readErr := reader.ReadSlice('\n')
		header.Write(line)
		if readErr != nil {
			return errors.Join(ErrProxyEntry, observed.failure(), readErr)
		}
		if bytes.Equal(line, []byte("\r\n")) {
			break
		}
	}
	if header.Len() > 16<<10 {
		return ErrProxyEntry
	}
	response, err := http.ReadResponse(bufio.NewReader(io.MultiReader(bytes.NewReader(header.Bytes()), reader)), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return errors.Join(ErrProxyEntry, observed.failure(), err)
	}
	// Close the owned socket before Body.Close: net/http otherwise drains an
	// arbitrarily large or stalled body even after our bounded read rejects it.
	defer func() { _ = connection.Close(); _ = response.Body.Close() }()
	if response.StatusCode != http.StatusProxyAuthRequired {
		return ErrProxyEntry
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(payload) > 4096 {
		return errors.Join(ErrProxyEntry, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ErrProxyEntry
	}
	fields := map[string]string{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || (key != "reasonCode" && key != "detailCode") {
			return ErrProxyEntry
		}
		if _, duplicate := fields[key]; duplicate {
			return ErrProxyEntry
		}
		var value string
		if decoder.Decode(&value) != nil {
			return ErrProxyEntry
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || fields["reasonCode"] != "proxy_authentication_required" {
		return ErrProxyEntry
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return ErrProxyEntry
	}
	return nil
}

const DoctorTimeout = 30 * time.Second

// RemoteDiagnosis contains only known outcomes, never a server response/error.
// A false Checked means that dependent operation was not performed.
type DoctorCheck struct {
	Checked bool
	Reason  string
}
type RemoteDiagnosis struct {
	Inspection  RemoteInspection
	Control     DoctorCheck
	Entry       DoctorCheck
	Environment DoctorCheck
}

func DiagnoseRemote(ctx context.Context, target serverconnection.Target, directory string, clock RemoteClock, selected environment.EnvironmentID) (RemoteDiagnosis, error) {
	var report RemoteDiagnosis
	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	check, cancel := context.WithTimeout(ctx, DoctorTimeout)
	inspection, err := inspectRemote(check, target, directory, clock, DoctorTimeout, true)
	cancel()
	report.Control.Checked = true
	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	if err != nil {
		report.Control.Reason = doctorReason(err, "control")
		return report, nil
	}
	report.Inspection = inspection
	report.Entry.Checked = true
	err = ProbeRemoteEntry(ctx, target, directory, clock, DoctorTimeout)
	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	if err != nil {
		report.Entry.Reason = doctorReason(err, "connect_ingress")
	}
	if selected != "" {
		report.Environment.Checked = true
		remote := RemoteConfig{Target: target, StateDirectory: directory, DisplayName: "vibermate-doctor", Clock: clock, Random: rand.Reader}
		launcher, newErr := New(Config{Remote: &remote, ControlTimeout: DoctorTimeout, BaseEnvironment: []string{}, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard})
		if newErr != nil {
			report.Environment.Reason = "environment_catalog"
			return report, nil
		}
		check, cancel = context.WithTimeout(ctx, DoctorTimeout)
		page, catalogErr := launcher.environments(check, true)
		cancel()
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		report.Environment.Reason = "environment_unavailable"
		if catalogErr != nil {
			report.Environment.Reason = doctorReason(catalogErr, "environment_catalog")
		} else {
			for _, item := range page.Items {
				if item.ID == selected {
					report.Environment.Reason = ""
					break
				}
			}
		}
	}
	return report, nil
}

func doctorReason(err error, fallback string) string {
	if errors.Is(err, serverconnection.ErrTrustNotConfigured) {
		return "trust_not_configured"
	}
	if errors.Is(err, ErrRemoteLoginRequired) || errors.Is(err, serverconnection.ErrLoginRequired) {
		return "login_or_machine"
	}
	if errors.Is(err, serverconnection.ErrInvalidCertificate) || errors.Is(err, serverconnection.ErrServerIdentityChanged) {
		return "tls_identity"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns"
	}
	var certificate x509.CertificateInvalidError
	var hostname x509.HostnameError
	var authority x509.UnknownAuthorityError
	var record tls.RecordHeaderError
	if errors.As(err, &certificate) || errors.As(err, &hostname) || errors.As(err, &authority) || errors.As(err, &record) {
		return "tls_identity"
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return "tcp"
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "timeout"
	}
	var failure *ControlFailure
	if errors.As(err, &failure) && failure.Status == http.StatusForbidden {
		return "permission"
	}
	return fallback
}
