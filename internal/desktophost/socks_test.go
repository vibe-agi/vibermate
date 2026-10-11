package desktophost_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

func TestDesktopSOCKSDelivery(t *testing.T) {
	for _, test := range []struct{ name, bind, kernel string }{{"ephemeral", "127.0.0.1:0", ""}, {"disabled", "", ""}, {"trusted kernel", "127.0.0.1:0", "/tmp/owned-trusted-xray"}} {
		t.Run(test.name, func(t *testing.T) { testDesktopSOCKSDelivery(t, test.bind, test.kernel) })
	}
}

func testDesktopSOCKSDelivery(t *testing.T, bind, kernel string) {
	options := hostOptions(t, newHostPaths(t, t.TempDir()), t.TempDir())
	options.SOCKSListenAddress = bind
	options.Runtime.VLESSExecutable = kernel
	h := startHost(t, options)
	defer shutdownHost(t, h)
	file, err := localdiscovery.NewFile(options.Paths.DiscoveryPath(), options.Runtime.Clock)
	if err != nil {
		t.Fatal(err)
	}
	session, err := file.Load()
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, session.BaseURL+"/api/v1/manual-captures/context?environmentId=system_transparent", nil)
	request.Header.Set("Authorization", "Bearer "+session.ControlCredential)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("context status=%d", response.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if got, _ := body["socksAddress"].(string); got != h.Status().SOCKSAddress {
		t.Fatalf("manual delivery SOCKS=%q want bound %q", got, h.Status().SOCKSAddress)
	}
	confirmation := body["confirmationToken"].(string)
	response = controlRequest(t, h.AppSession().BaseURL, http.MethodGet, "/api/v1/status", h.AppSession().ReadToken, "vibermate://desktop")
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	access, ok := body["networkAccess"].(map[string]any)
	got, _ := access["socksAddress"].(string)
	if !ok || got != h.Status().SOCKSAddress || access["vlessAvailable"] != (kernel != "") {
		t.Fatalf("network access=%v", body["networkAccess"])
	}
	payload, err := json.Marshal(capturecontrol.ManualCaptureCreateRequest{EnvironmentID: "system_transparent", DisplayName: "owned HTTP SOCKS grant", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked, ConfirmationToken: confirmation})
	if err != nil {
		t.Fatal(err)
	}
	request, _ = http.NewRequest(http.MethodPost, session.BaseURL+"/api/v1/manual-captures", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+session.ControlCredential)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var grant capturecontrol.ManualCaptureGrant
	if response.StatusCode != http.StatusCreated || json.NewDecoder(response.Body).Decode(&grant) != nil {
		t.Fatalf("create status=%d", response.StatusCode)
	}
	if grant.SOCKSAddress != h.Status().SOCKSAddress || grant.ProxyUsername != "capture" || grant.ProxyPassword == "" || !strings.HasPrefix(grant.ProxyAddress, "http://127.0.0.1:") {
		t.Fatal("grant changed local delivery/credential")
	}
	if bind != "" {
		connection, err := net.DialTimeout("tcp", grant.SOCKSAddress, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		connection.SetDeadline(time.Now().Add(3 * time.Second))
		connection.Write([]byte{5, 1, 2})
		var reply [2]byte
		if _, err := io.ReadFull(connection, reply[:]); err != nil || reply != [2]byte{5, 2} {
			t.Fatalf("SOCKS method=%v err=%v", reply, err)
		}
		auth := append([]byte{1, 7}, []byte(grant.ProxyUsername)...)
		auth = append(auth, byte(len(grant.ProxyPassword)))
		auth = append(auth, []byte(grant.ProxyPassword)...)
		connection.Write(auth)
		if _, err := io.ReadFull(connection, reply[:]); err != nil || reply != [2]byte{1, 0} {
			t.Fatalf("HTTP-issued Capture SOCKS auth=%v err=%v", reply, err)
		}
	}
}

func TestDesktopExplicitSOCKSListenerAndShutdown(t *testing.T) {
	options := hostOptions(t, newHostPaths(t, t.TempDir()), t.TempDir())
	field := reflect.ValueOf(&options).Elem().FieldByName("SOCKSListenAddress")
	if !field.IsValid() {
		t.Fatal("Desktop has no explicit SOCKS listener option")
	}
	field.SetString("127.0.0.1:0")
	h := startHost(t, options)
	defer shutdownHost(t, h)
	b, _ := json.Marshal(h.Status())
	var status struct {
		SOCKSAddress string `json:"socksAddress"`
	}
	json.Unmarshal(b, &status)
	if status.SOCKSAddress == "" {
		t.Fatal("Desktop omitted actual SOCKS address")
	}
	grant, e := h.Runtime().ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "host UDP", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if e != nil {
		t.Fatal(e)
	}
	c, e := net.DialTimeout("tcp", status.SOCKSAddress, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte{5, 1, 2})
	var reply [2]byte
	io.ReadFull(c, reply[:])
	if reply != [2]byte{5, 2} {
		t.Fatal(reply)
	}
	p := grant.Credential.Value()
	auth := append([]byte{1, 7}, []byte("capture")...)
	auth = append(auth, byte(len(p)))
	auth = append(auth, []byte(p)...)
	c.Write(auth)
	if _, e = io.ReadFull(c, reply[:]); e != nil || reply != [2]byte{1, 0} {
		t.Fatal("host real Runtime Capture auth", reply, e)
	}
	echo, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 100)
		n, a, e := echo.ReadFromUDP(b)
		if e == nil {
			echo.WriteToUDP(b[:n], a)
		}
	}()
	defer func() { echo.Close(); <-done }()
	peer := echo.LocalAddr().(*net.UDPAddr).AddrPort()
	if _, e = h.Runtime().ConnectionRules().Replace(t.Context(), h.Runtime().ConnectionRules().Current().Revision, []connectionpolicy.Rule{{ID: "owned.host-udp", Priority: 1, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("127.0.0.1", peer.Port())}}, connectionpolicy.ModeDenyUnknown); e != nil {
		t.Fatal(e)
	}
	u, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	defer u.Close()
	port := u.LocalAddr().(*net.UDPAddr).Port
	request := binary.BigEndian.AppendUint16([]byte{5, 3, 0, 1, 127, 0, 0, 1}, uint16(port))
	c.Write(request)
	var assoc [10]byte
	if _, e = io.ReadFull(c, assoc[:]); e != nil || assoc[1] != 0 {
		t.Fatal("host UDP ASSOCIATE", e)
	}
	relay := &net.UDPAddr{IP: net.IP(assoc[4:8]), Port: int(binary.BigEndian.Uint16(assoc[8:]))}
	packet := binary.BigEndian.AppendUint16([]byte{0, 0, 0, 1, 127, 0, 0, 1}, peer.Port())
	packet = append(packet, []byte{0, 255, 128, 9}...)
	u.SetDeadline(time.Now().Add(3 * time.Second))
	if _, e = u.WriteToUDP(packet, relay); e != nil {
		t.Fatal(e)
	}
	b = make([]byte, 100)
	n, _, e := u.ReadFromUDP(b)
	if e != nil || !bytes.Equal(b[:n], packet) {
		t.Fatal("host Runtime-backed packet echo", e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if e = h.Shutdown(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = io.ReadFull(c, reply[:]); e == nil {
		t.Fatal("shutdown retained authenticated SOCKS control")
	}
	if next, e := net.DialTimeout("tcp", status.SOCKSAddress, time.Second); e == nil {
		next.Close()
		t.Fatal("shutdown retained SOCKS listener")
	}
}

func TestDesktopDefaultNoSOCKSAndRejectsPublicBind(t *testing.T) {
	options := hostOptions(t, newHostPaths(t, t.TempDir()), t.TempDir())
	field := reflect.ValueOf(&options).Elem().FieldByName("SOCKSListenAddress")
	if !field.IsValid() {
		t.Fatal("missing explicit SOCKS option")
	}
	if field.String() != "" {
		t.Fatal("default opens SOCKS")
	}
	field.SetString("0.0.0.0:0")
	if h, e := desktophost.Start(t.Context(), options); e == nil {
		shutdownHost(t, h)
		t.Fatal("Desktop opened public SOCKS")
	}
}

func TestDesktopAttachedServerSOCKSDeliveryIsEmpty(t *testing.T) {
	options := hostOptions(t, newHostPaths(t, t.TempDir()), t.TempDir())
	options.SOCKSListenAddress = "127.0.0.1:0"
	options.Runtime.VLESSExecutable = "/tmp/owned-trusted-xray"
	options.RemoteServerEnabled = true
	options.RemoteServerListenAddress = "127.0.0.1:0"
	host := startHost(t, options)
	defer shutdownHost(t, host)
	remote := host.RemoteServerStatus()
	if remote.SOCKSAddress != "" || host.Status().SOCKSAddress == "" {
		t.Fatal("attached Server acquired or advertised Desktop socket")
	}
	key, err := os.ReadFile(remote.RecoveryKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + remote.ListenAddress
	payload, err := json.Marshal(servercontrol.WebSetup{Schema: servercontrol.WebSetupSchema, RecoveryKey: strings.TrimSpace(string(key)), Username: "owner", Password: "synthetic-owner-password"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(base+servercontrol.WebSetupPath, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var session servercontrol.WebSession
	if response.StatusCode != http.StatusCreated || json.NewDecoder(response.Body).Decode(&session) != nil {
		t.Fatalf("setup status=%d", response.StatusCode)
	}
	for _, path := range []string{"/api/v1/status", "/api/v1/manual-captures/context?environmentId=system_transparent"} {
		request, _ := http.NewRequest(http.MethodGet, base+path, nil)
		request.Header.Set("Authorization", "Bearer "+session.ReadToken)
		response, err := http.DefaultClient.Do(request)
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
			if _, present := body["socksAddress"]; present {
				t.Fatal("attached context advertises Desktop-local SOCKS")
			}
		} else {
			access, ok := body["networkAccess"].(map[string]any)
			if !ok || access["vlessAvailable"] != true {
				t.Fatalf("attached trusted availability=%v", access)
			}
			if _, present := access["socksAddress"]; present {
				t.Fatal("attached status advertises Desktop-local SOCKS")
			}
		}
	}
}

func TestDesktopOccupiedSOCKSRollsBackProxyListener(t *testing.T) {
	occupied, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer occupied.Close()
	reserved, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	proxy := reserved.Addr().String()
	reserved.Close()
	options := hostOptions(t, newHostPaths(t, t.TempDir()), t.TempDir())
	options.SOCKSListenAddress = occupied.Addr().String()
	options.ProxyListenAddress = proxy
	if h, e := desktophost.Start(t.Context(), options); e == nil {
		shutdownHost(t, h)
		t.Fatal("occupied SOCKS startup succeeded")
	}
	probe, e := net.Listen("tcp4", proxy)
	if e != nil {
		t.Fatal("partial SOCKS startup retained HTTP proxy", e)
	}
	probe.Close()
	options = hostOptions(t, options.Paths, options.Runtime.Paths.DataDirectory())
	options.SOCKSListenAddress = "127.0.0.1:0"
	h := startHost(t, options)
	shutdownHost(t, h)
}
