package serverhost_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

func TestServerSOCKSDelivery(t *testing.T) {
	for _, test := range []struct{ name, listen, advertised, kernel string }{
		{"ephemeral", "127.0.0.1:0", "", ""},
		{"disabled", "", "", ""},
		{"trusted kernel", "127.0.0.1:0", "", "/tmp/owned-trusted-xray"},
		{"advertised wildcard", "0.0.0.0:0", "Proxy.Example:01080", ""},
		{"IPv6", "[::1]:0", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) { testServerSOCKSDelivery(t, test.listen, test.advertised, test.kernel) })
	}
}

func testServerSOCKSDelivery(t *testing.T, listen, advertised, kernel string) {
	t.Helper()
	options := serverOptions(t, t.TempDir())
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	options.SOCKSListenAddress = listen
	options.SOCKSAccessAddress = advertised
	options.Runtime.VLESSExecutable = kernel
	host, err := serverhost.Start(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	key, err := os.ReadFile(host.Status().RecoveryKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	base := "http://" + host.Status().ListenAddress
	setup := postJSON(t, client, base+servercontrol.WebSetupPath, "", servercontrol.WebSetup{Schema: servercontrol.WebSetupSchema, RecoveryKey: strings.TrimSpace(string(key)), Username: "owner", Password: "synthetic-owner-password"})
	defer setup.Body.Close()
	if setup.StatusCode != http.StatusCreated {
		t.Fatalf("setup=%d", setup.StatusCode)
	}
	var session servercontrol.WebSession
	if err := json.NewDecoder(setup.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	want := host.Status().SOCKSAddress
	if advertised != "" {
		want = "proxy.example:1080"
	}
	var reviewed map[string]any
	for _, path := range []string{"/api/v1/manual-captures/context?environmentId=system_transparent", "/api/v1/status"} {
		request, _ := http.NewRequest(http.MethodGet, base+path, nil)
		request.Header.Set("Authorization", "Bearer "+session.ReadToken)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		err = json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%d err=%v", path, response.StatusCode, err)
		}
		if strings.Contains(path, "context") {
			reviewed = body
			if got, _ := body["socksAddress"].(string); got != want {
				t.Fatalf("manual delivery SOCKS=%q want bound %q", got, want)
			}
		} else {
			access, ok := body["networkAccess"].(map[string]any)
			got, _ := access["socksAddress"].(string)
			if !ok || got != want || access["vlessAvailable"] != (kernel != "") {
				t.Fatalf("network access=%v", body["networkAccess"])
			}
		}
	}
	created := postJSON(t, client, base+"/api/v1/manual-captures", session.WriteToken, capturecontrol.ManualCaptureCreateRequest{EnvironmentID: "system_transparent", DisplayName: "owned synthetic SOCKS", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked, ConfirmationToken: reviewed["confirmationToken"].(string)})
	defer created.Body.Close()
	var grant capturecontrol.ManualCaptureGrant
	if created.StatusCode != http.StatusCreated || json.NewDecoder(created.Body).Decode(&grant) != nil {
		t.Fatalf("create status=%d", created.StatusCode)
	}
	if grant.SOCKSAddress != want || grant.ProxyAddress != reviewed["proxyAddress"] || grant.ProxyUsername != "capture" || grant.ProxyPassword == "" {
		t.Fatal("created grant changed reviewed endpoints/credential")
	}
	if listen != "" {
		if reply := socksDeliveryAuth(t, host.Status().SOCKSAddress, grant.ProxyUsername, grant.ProxyPassword); reply != [2]byte{1, 0} {
			t.Fatalf("Capture SOCKS auth=%v", reply)
		}
		if reply := socksDeliveryAuth(t, host.Status().SOCKSAddress, "capture", session.WriteToken); reply == [2]byte{1, 0} {
			t.Fatal("owner token became SOCKS credential")
		}
	}
	for _, path := range []string{"/api/v1/manual-captures", "/api/v1/manual-captures/" + grant.Capture.ID, "/api/v1/status"} {
		request, _ := http.NewRequest(http.MethodGet, base+path, nil)
		request.Header.Set("Authorization", "Bearer "+session.ReadToken)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("readback status=%d err=%v", response.StatusCode, err)
		}
		if strings.Contains(string(payload), grant.ProxyPassword) || strings.Contains(string(payload), "proxyPassword") {
			t.Fatal("ordinary read/status disclosed proxy password")
		}
		if path != "/api/v1/status" && strings.Contains(string(payload), "socksAddress") {
			t.Fatal("ordinary manual Capture view carries grant delivery")
		}
	}
	request, _ := http.NewRequest(http.MethodPost, base+"/api/v1/manual-captures/"+grant.Capture.ID+"/actions/rotate-credential", nil)
	request.Header.Set("Authorization", "Bearer "+session.WriteToken)
	request.Header.Set("If-Match", created.Header.Get("ETag"))
	rotatedResponse, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer rotatedResponse.Body.Close()
	var rotated capturecontrol.ManualCaptureGrant
	if rotatedResponse.StatusCode != http.StatusOK || json.NewDecoder(rotatedResponse.Body).Decode(&rotated) != nil {
		t.Fatalf("rotate status=%d", rotatedResponse.StatusCode)
	}
	if rotated.SOCKSAddress != want || rotated.ProxyAddress != grant.ProxyAddress || rotated.ProxyPassword == grant.ProxyPassword || rotated.ProxyPassword == "" {
		t.Fatal("rotation changed delivery or kept credential")
	}
	if listen != "" {
		if reply := socksDeliveryAuth(t, host.Status().SOCKSAddress, "capture", grant.ProxyPassword); reply == [2]byte{1, 0} {
			t.Fatal("rotation retained old SOCKS credential")
		}
		if reply := socksDeliveryAuth(t, host.Status().SOCKSAddress, rotated.ProxyUsername, rotated.ProxyPassword); reply != [2]byte{1, 0} {
			t.Fatalf("rotated SOCKS auth=%v", reply)
		}
	}
}

func socksDeliveryAuth(t *testing.T, address, username, password string) [2]byte {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := connection.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(connection, reply[:]); err != nil || reply != [2]byte{5, 2} {
		t.Fatalf("SOCKS method=%v err=%v", reply, err)
	}
	auth := append([]byte{1, byte(len(username))}, []byte(username)...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, []byte(password)...)
	if _, err := connection.Write(auth); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(connection, reply[:]); err != nil {
		t.Fatal(err)
	}
	return reply
}
