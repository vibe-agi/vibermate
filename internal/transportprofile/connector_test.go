package transportprofile

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accessapply"
)

func TestConnectorReplaysObservedShapeWithFreshConnectionState(t *testing.T) {
	t.Parallel()

	downstream := captureGoClientHello(
		t,
		"agent.example",
		[]string{"h2", "http/1.1"},
	)
	roots, serverConfig := testTLSAuthority(t)
	dialer := newPipeTLSDialer(serverConfig)
	connector, err := NewConnector(ConnectorOptions{
		Dialer:           dialer,
		RootCAs:          roots,
		HandshakeTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := ConnectRequest{
		Network:       "tcp",
		Address:       "example.com:443",
		TLSServerName: "example.com",
		Plan:          testTransportPlan(t),
		Observation:   downstream,
	}
	if _, _, reason, err := prepareObservedSpec(
		downstream,
		request.Plan.Requested(),
		request.TLSServerName,
	); err != nil {
		t.Fatalf("prepare observed ClientHello reason=%s error=%v", reason, err)
	}
	first, firstEvidence, err := connector.Connect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	firstUpstream := dialer.nextObservation(t)

	second, secondEvidence, err := connector.Connect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	secondUpstream := dialer.nextObservation(t)

	for _, evidence := range []Evidence{firstEvidence, secondEvidence} {
		chain := evidence.FallbackChain()
		if evidence.Requested().Ref !=
			access.TransportProfileObservedClientH1Value ||
			evidence.Effective().Ref !=
				access.TransportProfileObservedClientH1Value ||
			len(chain) != 1 ||
			chain[0] != evidence.Requested() ||
			evidence.UsedFallback() ||
			!slicesEqual(
				evidence.UpstreamOfferedALPN(),
				[]string{"http/1.1"},
			) ||
			evidence.UpstreamNegotiatedALPN() != "http/1.1" ||
			evidence.HTTPTransport() != access.HTTPTransportHTTP1 {
			t.Fatalf("transport evidence = %+v", evidence)
		}
	}
	if got := clientHelloServerName(t, firstUpstream.fingerprintRecord); got != "example.com" {
		t.Fatalf("upstream SNI = %q", got)
	}
	if !slicesEqual(
		firstUpstream.OfferedALPN(),
		[]string{"http/1.1"},
	) {
		t.Fatalf("upstream ALPN = %v", firstUpstream.OfferedALPN())
	}
	if bytes.Equal(
		clientHelloRandom(t, downstream.fingerprintRecord),
		clientHelloRandom(t, firstUpstream.fingerprintRecord),
	) {
		t.Fatal("upstream ClientHello reused the downstream random")
	}
	if bytes.Equal(
		clientHelloRandom(t, firstUpstream.fingerprintRecord),
		clientHelloRandom(t, secondUpstream.fingerprintRecord),
	) {
		t.Fatal("two upstream connections reused a ClientHello random")
	}
	if bytes.Equal(
		clientHelloKeyShare(t, firstUpstream.fingerprintRecord),
		clientHelloKeyShare(t, secondUpstream.fingerprintRecord),
	) {
		t.Fatal("two upstream connections reused key share bytes")
	}
}

func TestConnectorUsesDeterministicStandardFallbackWithoutObservation(
	t *testing.T,
) {
	t.Parallel()

	roots, serverConfig := testTLSAuthority(t)
	dialer := newPipeTLSDialer(serverConfig)
	connector, err := NewConnector(ConnectorOptions{
		Dialer:           dialer,
		RootCAs:          roots,
		HandshakeTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, evidence, err := connector.Connect(
		context.Background(),
		ConnectRequest{
			Network:       "tcp",
			Address:       "example.com:443",
			TLSServerName: "example.com",
			Plan:          testTransportPlan(t),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	_ = dialer.nextObservation(t)
	chain := evidence.FallbackChain()
	if evidence.Effective().Ref !=
		access.TransportProfileStandardH1Value ||
		len(chain) != 2 ||
		chain[0].Ref != access.TransportProfileObservedClientH1Value ||
		chain[1].Ref != access.TransportProfileStandardH1Value ||
		evidence.FallbackReason() != FallbackObservationUnavailable ||
		!evidence.UsedFallback() {
		t.Fatalf("fallback evidence = %+v", evidence)
	}
	chain[0].Ref = "mutated"
	cloned := evidence.Clone()
	if evidence.FallbackChain()[0].Ref !=
		access.TransportProfileObservedClientH1Value ||
		cloned.FallbackChain()[0].Ref !=
			access.TransportProfileObservedClientH1Value {
		t.Fatal("fallback evidence exposed an alias")
	}
}

func TestConnectorNeverFallsBackAroundHostnameVerification(t *testing.T) {
	t.Parallel()

	downstream := captureGoClientHello(
		t,
		"agent.example",
		[]string{"http/1.1"},
	)
	roots, serverConfig := testTLSAuthority(t)
	dialer := newPipeTLSDialer(serverConfig)
	connector, err := NewConnector(ConnectorOptions{
		Dialer:           dialer,
		RootCAs:          roots,
		HandshakeTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, _, err := connector.Connect(
		context.Background(),
		ConnectRequest{
			Network:       "tcp",
			Address:       "wrong.invalid:443",
			TLSServerName: "wrong.invalid",
			Plan:          testTransportPlan(t),
			Observation:   downstream,
		},
	)
	if err == nil || connection != nil {
		t.Fatal("hostname mismatch unexpectedly established a connection")
	}
	if dialer.dialCount() != 1 {
		t.Fatalf(
			"hostname failure attempted %d profiles, want 1",
			dialer.dialCount(),
		)
	}
}

func TestConnectorHandshakeCancellationConverges(t *testing.T) {
	t.Parallel()

	connector, err := NewConnector(ConnectorOptions{
		Dialer:           hangingDialer{},
		HandshakeTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, connectErr := connector.Connect(ctx, ConnectRequest{
			Network:       "tcp",
			Address:       "example.com:443",
			TLSServerName: "example.com",
			Plan:          testTransportPlan(t),
		})
		result <- connectErr
	}()
	time.Sleep(10 * time.Millisecond)
	cause := errors.New("TLS operation canceled")
	cancel(cause)
	select {
	case err := <-result:
		if !errors.Is(err, cause) {
			t.Fatalf("Connect() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("TLS handshake did not converge after cancellation")
	}
}

func captureGoClientHello(
	t *testing.T,
	serverName string,
	alpn []string,
) Observation {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	clientDone := make(chan error, 1)
	go func() {
		secured := tls.Client(clientSide, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: serverName,
			NextProtos: alpn,
		})
		clientDone <- secured.Handshake()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observation, replay, err := CaptureClientHello(
		ctx,
		serverSide,
		DefaultMaxClientHelloBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = replay.Close()
	_ = clientSide.Close()
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("test TLS client did not stop")
	}
	return observation
}

func testTLSAuthority(t *testing.T) (*x509.CertPool, *tls.Config) {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
	}))
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	config := server.TLS.Clone()
	config.NextProtos = []string{"http/1.1"}
	return roots, config
}

type pipeTLSResult struct {
	observation Observation
	err         error
}

type pipeTLSDialer struct {
	config *tls.Config

	mu      sync.Mutex
	dials   int
	results chan pipeTLSResult
}

func newPipeTLSDialer(config *tls.Config) *pipeTLSDialer {
	return &pipeTLSDialer{
		config:  config.Clone(),
		results: make(chan pipeTLSResult, 8),
	}
}

func (dialer *pipeTLSDialer) DialContext(
	ctx context.Context,
	_ string,
	_ string,
) (net.Conn, error) {
	clientSide, serverSide := net.Pipe()
	dialer.mu.Lock()
	dialer.dials++
	dialer.mu.Unlock()
	go func() {
		observation, replay, err := CaptureClientHello(
			ctx,
			serverSide,
			DefaultMaxClientHelloBytes,
		)
		var secured *tls.Conn
		if err == nil {
			secured = tls.Server(replay, dialer.config.Clone())
			err = secured.HandshakeContext(ctx)
		}
		dialer.results <- pipeTLSResult{
			observation: observation,
			err:         err,
		}
		if err == nil {
			var terminal [1]byte
			_, _ = secured.Read(terminal[:])
		}
		if replay != nil {
			_ = replay.Close()
		} else {
			_ = serverSide.Close()
		}
	}()
	return clientSide, nil
}

func (dialer *pipeTLSDialer) nextObservation(t *testing.T) Observation {
	t.Helper()
	select {
	case result := <-dialer.results:
		if result.err != nil {
			t.Fatalf("test TLS server error = %v", result.err)
		}
		return result.observation
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upstream ClientHello")
		return Observation{}
	}
}

func (dialer *pipeTLSDialer) dialCount() int {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	return dialer.dials
}

type hangingDialer struct{}

func (hangingDialer) DialContext(
	ctx context.Context,
	_ string,
	_ string,
) (net.Conn, error) {
	clientSide, serverSide := net.Pipe()
	go func() {
		<-ctx.Done()
		_ = serverSide.Close()
	}()
	return clientSide, nil
}

func testTransportPlan(t *testing.T) access.CompiledTransportFingerprintPlan {
	t.Helper()
	command, err := accessapply.BuildCommand("transport-test", accessapply.Input{
		ExpectedRevision: 0,
		Access: accessapply.AccessInput{
			ID:                "transport-test",
			Name:              "Transport test",
			Status:            string(access.AccessStatusEnabled),
			AgentEndpointID:   "agent",
			DefaultRouteSetID: "route",
			ProfileIDs:        []string{"profile"},
			EgressPolicyID:    "egress",
		},
		AgentEndpoint: accessapply.AgentEndpointInput{
			ID:            "agent",
			ClientOrigin:  "https://agent.example:443",
			ClientDialect: string(access.DialectAnthropicMessages),
		},
		Profiles: []accessapply.ProfileInput{{
			ID:                  "profile",
			Name:                "Provider",
			BackendDialect:      string(access.DialectOpenAIChat),
			TargetID:            "target",
			TransportProfileRef: access.TransportProfileObservedClientH1Value,
			DefaultModelPolicy: accessapply.ModelPolicyInput{
				Mode:       string(access.ModelPolicyModeFixed),
				FixedModel: "provider-model",
			},
			AccountBindingIDs:       []string{"account"},
			DefaultAccountBindingID: "account",
		}},
		ProviderTargets: []accessapply.ProviderTargetInput{{
			ID:        "target",
			ProfileID: "profile",
			Origin:    "https://example.com:443/v1",
			Protocol:  string(access.DialectOpenAIChat),
			Capabilities: []string{
				string(access.ProviderCapabilityMessages),
				string(access.ProviderCapabilityStreaming),
				string(access.ProviderCapabilityToolCalls),
			},
		}},
		AccountBindings: []accessapply.AccountBindingInput{{
			ID:            "account",
			ProfileID:     "profile",
			Label:         "Provider",
			SecretRef:     "secret://provider/account",
			AuthDriverRef: access.AuthDriverStaticHeaderValue,
			Enabled:       true,
		}},
		RouteSets: []accessapply.RouteSetInput{{
			ID:                  "route",
			CandidateProfileIDs: []string{"profile"},
		}},
		EgressPolicy: accessapply.EgressPolicyInput{
			ID:   "egress",
			Mode: string(access.EgressModeDirect),
		},
		PluginPlan: accessapply.PluginPlanInput{
			Mode: string(access.PluginPlanModePassThrough),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	codecID, err := access.NewCodecPairID(
		"anthropic-messages-to-openai-chat",
	)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := access.NewCatalog(access.CatalogOptions{
		Capabilities: access.PlanCapabilities{
			MaxEndpointProfiles: 1,
			MaxAccountBindings:  1,
			MaxRouteSets:        1,
		},
		CodecPairs: []access.CodecPairDefinition{{
			ID:              codecID,
			Revision:        1,
			ClientDialect:   access.DialectAnthropicMessages,
			ProviderDialect: access.DialectOpenAIChat,
			RequiredCapabilities: []access.ProviderCapability{
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
			},
		}},
		AuthDrivers: []access.AuthDriverDefinition{{
			Ref:      access.StaticHeaderAuthDriverRef(),
			Revision: 1,
		}},
		EgressModes: []access.EgressModeDefinition{{
			Mode:     access.EgressModeDirect,
			Revision: 1,
		}},
		PluginPlanModes: []access.PluginPlanModeDefinition{{
			Mode:     access.PluginPlanModePassThrough,
			Revision: 1,
		}},
		ModelPolicyModes: []access.ModelPolicyModeDefinition{{
			Mode:     access.ModelPolicyModeFixed,
			Revision: 1,
		}},
		TransportProfiles: []access.TransportFingerprintDefinition{
			access.ObservedClientH1TransportFingerprintDefinition(),
			access.StandardH1TransportFingerprintDefinition(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := access.NewCompiler(catalog)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := compiler.Compile(command.Aggregate)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.TransportFingerprintPlan()
}

func clientHelloRandom(t *testing.T, record []byte) []byte {
	t.Helper()
	if len(record) < 43 {
		t.Fatal("ClientHello is too short for random")
	}
	return bytes.Clone(record[11:43])
}

func clientHelloServerName(t *testing.T, record []byte) string {
	t.Helper()
	payload := clientHelloExtension(t, record, 0)
	if len(payload) < 5 {
		t.Fatal("SNI extension is truncated")
	}
	nameBytes := int(payload[3])<<8 | int(payload[4])
	if len(payload) != 5+nameBytes {
		t.Fatal("SNI extension length is invalid")
	}
	return string(payload[5:])
}

func clientHelloKeyShare(t *testing.T, record []byte) []byte {
	t.Helper()
	payload := clientHelloExtension(t, record, 51)
	if len(payload) < 6 {
		t.Fatal("key share extension is truncated")
	}
	listBytes := int(payload[0])<<8 | int(payload[1])
	if len(payload) != listBytes+2 {
		t.Fatal("key share list length is invalid")
	}
	keyBytes := int(payload[4])<<8 | int(payload[5])
	if len(payload) < 6+keyBytes {
		t.Fatal("key share is truncated")
	}
	return bytes.Clone(payload[6 : 6+keyBytes])
}

func clientHelloExtension(t *testing.T, record []byte, id uint16) []byte {
	t.Helper()
	if len(record) < 9 {
		t.Fatal("ClientHello record is truncated")
	}
	body := record[9:]
	cursor := byteCursor{data: body}
	if !cursor.skip(2 + 32) {
		t.Fatal("ClientHello body is truncated")
	}
	if _, ok := cursor.vector8(); !ok {
		t.Fatal("ClientHello session ID is truncated")
	}
	if _, ok := cursor.vector16(); !ok {
		t.Fatal("ClientHello ciphers are truncated")
	}
	if _, ok := cursor.vector8(); !ok {
		t.Fatal("ClientHello compression is truncated")
	}
	extensions, ok := cursor.vector16()
	if !ok {
		t.Fatal("ClientHello extensions are truncated")
	}
	extensionCursor := byteCursor{data: extensions}
	for extensionCursor.remaining() != 0 {
		extensionID, ok := extensionCursor.uint16()
		if !ok {
			t.Fatal("ClientHello extension ID is truncated")
		}
		payload, ok := extensionCursor.vector16()
		if !ok {
			t.Fatal("ClientHello extension is truncated")
		}
		if extensionID == id {
			return bytes.Clone(payload)
		}
	}
	t.Fatalf("ClientHello extension %d is absent", id)
	return nil
}
