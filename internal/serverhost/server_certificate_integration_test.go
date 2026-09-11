package serverhost_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

func TestExportedServerCertificateEstablishesStrictHTTPSForConfiguredHost(t *testing.T) {
	options := serverOptions(t, t.TempDir())
	options.Transport.TLSHosts = []string{"vibermate.example.test", "192.168.1.20"}
	host, err := serverhost.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	status := host.Status()
	client := tlsHTTPClient(t)
	target := "https://" + status.ListenAddress + servercontrol.ServerCertificatePath
	unauthorized, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatal("certificate API accepted unauthenticated access")
	}
	recovery, err := os.ReadFile(status.RecoveryKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	login := postJSON(t, client, "https://"+status.ListenAddress+servercontrol.AdminSessionPath, "",
		servercontrol.AdminLogin{Schema: servercontrol.AdminLoginSchema, AccessKey: strings.TrimSpace(string(recovery))})
	defer login.Body.Close()
	if login.StatusCode != http.StatusCreated {
		t.Fatalf("login: %d", login.StatusCode)
	}
	var session servercontrol.AdminSession
	if err := json.NewDecoder(login.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer "+session.ReadToken)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("export: %d", response.StatusCode)
	}
	var exported servercontrol.ServerCertificate
	if err := json.NewDecoder(response.Body).Decode(&exported); err != nil {
		t.Fatal(err)
	}
	if exported.Fingerprint != status.TLSFingerprint || strings.Contains(exported.CertificatePEM, "PRIVATE KEY") {
		t.Fatal("download does not match the active public TLS identity")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(exported.CertificatePEM)) {
		t.Fatal("download is not usable as a trusted certificate")
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, status.ListenAddress)
		},
	}
	defer transport.CloseIdleConnections()
	verifiedClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	verified, err := verifiedClient.Get("https://vibermate.example.test" + servercontrol.WebAuthPath)
	if err != nil {
		t.Fatalf("downloaded certificate cannot establish strict HTTPS: %v", err)
	}
	defer verified.Body.Close()
	_, _ = io.Copy(io.Discard, verified.Body)
	if verified.StatusCode != http.StatusOK {
		t.Fatalf("verified response: %d", verified.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer "+session.ReadToken)
	request.Header.Set("Origin", "https://another.example.test")
	crossOrigin, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	crossOrigin.Body.Close()
	if crossOrigin.StatusCode != http.StatusUnauthorized {
		t.Fatal("certificate API accepted a cross-origin request")
	}
}

func TestServerCertificateOnlineApplyUsesOwnerWriteAndPreservesListener(t *testing.T) {
	root := t.TempDir()
	host, err := serverhost.Start(context.Background(), serverOptions(t, root))
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	status := host.Status()
	base := "https://" + status.ListenAddress
	client := tlsHTTPClient(t)
	recovery, err := os.ReadFile(status.RecoveryKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	setup := postJSON(t, client, base+servercontrol.WebSetupPath, "", servercontrol.WebSetup{
		Schema: servercontrol.WebSetupSchema, RecoveryKey: strings.TrimSpace(string(recovery)), Username: "owner", Password: "correct horse battery staple",
	})
	defer setup.Body.Close()
	if setup.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d", setup.StatusCode)
	}
	var owner servercontrol.WebSession
	if err := json.NewDecoder(setup.Body).Decode(&owner); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, base+servercontrol.ServerCertificatePath, nil)
	request.Header.Set("Authorization", "Bearer "+owner.ReadToken)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var initial servercontrol.ServerCertificate
	if err := json.NewDecoder(response.Body).Decode(&initial); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !initial.Managed {
		t.Fatal("self-signed listener is not managed")
	}
	if initial.CA == nil || !initial.IssuedByCA {
		t.Fatal("fresh managed installation has no HTTPS CA")
	}
	trafficRoot, err := os.ReadFile(filepath.Join(root, "data", "local-ca", "root-certificate.pem"))
	if err != nil || initial.CA.CertificatePEM != string(trafficRoot) || initial.CA.Schema != "vibermate-runtime-root-ca-v1" {
		t.Fatal("downloaded CA is not the exact traffic Root")
	}
	if _, err := os.Stat(filepath.Join(root, "data", "server-transport", "server-tls-ca.json")); !os.IsNotExist(err) {
		t.Fatal("fresh server created a second signing CA")
	}
	stageInput := map[string]any{
		"schema": servercontrol.ServerCertificateStageSchema, "hosts": []string{"192.168.1.20", "runtime.example.test"},
		"expectedFingerprint": initial.Fingerprint, "expectedPendingFingerprint": "",
	}
	created := postJSON(t, client, base+servercontrol.RuntimeUsersPath, owner.WriteToken, servercontrol.RuntimeUserCreate{
		Schema: servercontrol.RuntimeUserCreateSchema, Username: "member", Password: "member fixture password",
	})
	created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create member: %d", created.StatusCode)
	}
	login := postJSON(t, client, base+servercontrol.WebSessionPath, "", servercontrol.WebLogin{
		Schema: servercontrol.WebLoginSchema, Username: "member", Password: "member fixture password",
	})
	var member servercontrol.WebSession
	if err := json.NewDecoder(login.Body).Decode(&member); err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	for _, token := range []string{"", owner.ReadToken, member.WriteToken} {
		denied := postJSON(t, client, base+servercontrol.ServerCertificateStagePath, token, stageInput)
		denied.Body.Close()
		if denied.StatusCode != http.StatusUnauthorized {
			t.Fatalf("certificate write permission: %d", denied.StatusCode)
		}
	}
	staged := postJSON(t, client, base+servercontrol.ServerCertificateStagePath, owner.WriteToken, stageInput)
	if staged.StatusCode != http.StatusOK {
		t.Fatalf("stage: %d", staged.StatusCode)
	}
	var prepared servercontrol.ServerCertificate
	if err := json.NewDecoder(staged.Body).Decode(&prepared); err != nil {
		t.Fatal(err)
	}
	staged.Body.Close()
	if prepared.Pending == nil || prepared.Fingerprint != initial.Fingerprint || host.Status().TLSFingerprint != initial.Fingerprint {
		t.Fatal("staging affected current TLS")
	}
	conflict := postJSON(t, client, base+servercontrol.ServerCertificateStagePath, owner.WriteToken, stageInput)
	conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatal("stale edit overwrote pending certificate")
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(initial.CertificatePEM))
	// Hold a genuinely established TLS connection across the application.
	established, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", status.ListenAddress, &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	defer established.Close()
	established.SetDeadline(time.Now().Add(10 * time.Second))
	applied := postJSON(t, client, base+servercontrol.ServerCertificateApplyPath, owner.WriteToken, map[string]any{
		"schema":              servercontrol.ServerCertificateApplySchema,
		"expectedFingerprint": initial.Fingerprint, "pendingFingerprint": prepared.Pending.Fingerprint,
	})
	defer applied.Body.Close()
	if applied.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d", applied.StatusCode)
	}
	var current servercontrol.ServerCertificate
	if err := json.NewDecoder(applied.Body).Decode(&current); err != nil {
		t.Fatal(err)
	}
	if current.Pending != nil || current.Fingerprint != prepared.Pending.Fingerprint || host.Status().TLSFingerprint != current.Fingerprint || host.Status().InstanceID != status.InstanceID || host.Status().ListenAddress != status.ListenAddress {
		t.Fatal("identity or listener status not updated atomically")
	}
	heldRequest, _ := http.NewRequest(http.MethodGet, base+servercontrol.WebAuthPath, nil)
	if err := heldRequest.Write(established); err != nil {
		t.Fatal(err)
	}
	heldResponse, err := http.ReadResponse(bufio.NewReader(established), heldRequest)
	if err != nil {
		t.Fatalf("established TLS connection interrupted: %v", err)
	}
	io.Copy(io.Discard, heldResponse.Body)
	heldResponse.Body.Close()
	if heldResponse.StatusCode != http.StatusOK {
		t.Fatal("held TLS request failed")
	}
	strictClient := func(pem string) *http.Client {
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM([]byte(pem))
		transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}, DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, status.ListenAddress)
			},
		}
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 5 * time.Second}
	}
	oldConnection, err := strictClient(initial.CertificatePEM).Get("https://localhost" + servercontrol.WebAuthPath)
	if err == nil {
		oldConnection.Body.Close()
		t.Fatal("new TLS connection still accepts only the old self-signed certificate")
	}
	if current.CA == nil || current.CA.Fingerprint != initial.CA.Fingerprint || !current.IssuedByCA {
		t.Fatal("applying a certificate replaced its HTTPS CA")
	}
	for _, trust := range []string{current.CertificatePEM, initial.CA.CertificatePEM} {
		for _, address := range []string{"localhost", "192.168.1.20", "runtime.example.test"} {
			verified, err := strictClient(trust).Get("https://" + address + servercontrol.WebAuthPath)
			if err != nil {
				t.Fatalf("strict HTTPS %s: %v", address, err)
			}
			verified.Body.Close()
			if verified.StatusCode != http.StatusOK {
				t.Fatalf("verified request: %d", verified.StatusCode)
			}
		}
	}
	// Reuse the exact CA-only trust store across another live IP change.
	caClient := strictClient(initial.CA.CertificatePEM)
	secondStage := postJSON(t, caClient, base+servercontrol.ServerCertificateStagePath, owner.WriteToken, map[string]any{
		"schema": servercontrol.ServerCertificateStageSchema, "hosts": []string{"192.168.1.30"},
		"expectedFingerprint": current.Fingerprint, "expectedPendingFingerprint": "",
	})
	if secondStage.StatusCode != http.StatusOK {
		t.Fatalf("second stage: %d", secondStage.StatusCode)
	}
	var next servercontrol.ServerCertificate
	if err := json.NewDecoder(secondStage.Body).Decode(&next); err != nil {
		t.Fatal(err)
	}
	secondStage.Body.Close()
	secondApply := postJSON(t, caClient, base+servercontrol.ServerCertificateApplyPath, owner.WriteToken, map[string]any{
		"schema": servercontrol.ServerCertificateApplySchema, "expectedFingerprint": current.Fingerprint,
		"pendingFingerprint": next.Pending.Fingerprint,
	})
	if secondApply.StatusCode != http.StatusOK {
		t.Fatalf("second apply: %d", secondApply.StatusCode)
	}
	if err := json.NewDecoder(secondApply.Body).Decode(&current); err != nil {
		t.Fatal(err)
	}
	secondApply.Body.Close()
	verified, err := caClient.Get("https://192.168.1.30" + servercontrol.WebAuthPath)
	if err != nil {
		t.Fatalf("same CA cannot verify a changed server IP: %v", err)
	}
	verified.Body.Close()
	if mismatch, err := caClient.Get("https://192.168.1.20" + servercontrol.WebAuthPath); err == nil {
		mismatch.Body.Close()
		t.Fatal("CA trust bypassed hostname mismatch after IP removal")
	}
	shutdownServer(t, host)
	restarted, err := serverhost.Start(context.Background(), serverOptions(t, root))
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, restarted)
	if restarted.Status().TLSFingerprint != current.Fingerprint {
		t.Fatal("restart reset the UI-configured certificate")
	}
	status = restarted.Status()
	verified, err = caClient.Get("https://192.168.1.30" + servercontrol.WebAuthPath)
	if err != nil {
		t.Fatalf("same CA cannot verify server after restart: %v", err)
	}
	verified.Body.Close()
}
