//go:build linux || darwin

package flowbroker

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/vibe-agi/vibermate/internal/capturescope"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vibe-agi/vibermate/internal/processlineage"
)

const (
	requestTimeout = 2 * time.Second
	flowOwned      = 'Y'
	flowUnowned    = 'N'
	// flowDirect answers an owned process's flow whose destination its run's
	// capture scope keeps off ViberMate (ADR 0025): the extension declines it
	// but still counts the process as owned.
	flowDirect    = 'D'
	helloAccepted = 'H'
	// extensionFreshness is how recent a hello must be for the extension to
	// count as active; it sends one at least every 10 seconds. It covers an
	// extension that hangs while its liveness stream stays open.
	extensionFreshness = 30 * time.Second
)

// livenessInterval is how often the broker re-evaluates whether the
// extension is active and sends each registration a keepalive.
const livenessInterval = time.Second

// Broker attributes intercepted flows to Capture Runs and hands each owned
// flow's connection to the run that registered its process lineage.
type Broker struct {
	lineage *processlineage.Registry
	reader  processlineage.Reader
	// allowFlowPeer decides which Unix-socket peers may submit flows, so a
	// same-user process cannot route traffic under another run's identity.
	// Loopback TCP submissions authenticate with a secret instead.
	allowFlowPeer func(Peer) bool

	sequence      atomic.Uint64
	lastHello     atomic.Int64
	queryFailures atomic.Uint64
	now           func() time.Time
	interval      time.Duration
	stop          chan struct{}
	stopOnce      sync.Once

	mu       sync.Mutex
	channels map[string]*runChannel
	streams  int
}

// Stats is the broker's view of the capture extension, for Host status.
type Stats struct {
	ExtensionActive bool
	// QueryFailures is the extension's reported count of ownership queries
	// it could not complete. Such a flow from a process never seen before is
	// the remaining way an owned connection can leave uncaptured (ADR 0024).
	QueryFailures uint64
}

type runChannel struct {
	mu   sync.Mutex
	conn *net.UnixConn
	// scope is the run's address rules; it decides which owned flows go
	// direct (ADR 0025).
	scope capturescope.Scope
}

func New(reader processlineage.Reader, allowFlowPeer func(Peer) bool) (*Broker, error) {
	return newBroker(reader, allowFlowPeer, time.Now, livenessInterval)
}

func newBroker(reader processlineage.Reader, allowFlowPeer func(Peer) bool, now func() time.Time, interval time.Duration) (*Broker, error) {
	if reader == nil || allowFlowPeer == nil || now == nil || interval <= 0 {
		return nil, errors.New("flow broker reader and flow peer policy are required")
	}
	broker := &Broker{
		lineage:       processlineage.NewRegistry(reader),
		reader:        reader,
		allowFlowPeer: allowFlowPeer,
		channels:      map[string]*runChannel{},
		now:           now,
		interval:      interval,
		stop:          make(chan struct{}),
	}
	go broker.monitor()
	return broker, nil
}

// Stats reports the extension's liveness and query failures.
func (broker *Broker) Stats() Stats {
	return Stats{ExtensionActive: broker.extensionActive(), QueryFailures: broker.queryFailures.Load()}
}

// monitor revokes every registration when the extension stops being active
// while vibermated keeps running: its stream closed and no hello is fresh.
// Each run then pauses its Agent instead of letting new connections leave
// uncaptured.
func (broker *Broker) monitor() {
	ticker := time.NewTicker(broker.interval)
	defer ticker.Stop()
	for {
		select {
		case <-broker.stop:
			return
		case <-ticker.C:
			broker.checkLiveness()
		}
	}
}

// checkLiveness keeps no claim open while the extension is inactive. A run
// refuses a claim granted without an active extension, so this only ends
// claims whose capture has stopped working.
func (broker *Broker) checkLiveness() {
	if !broker.extensionActive() {
		broker.revokeRegistrations()
	}
}

func (broker *Broker) revokeRegistrations() {
	broker.mu.Lock()
	channels := make([]*runChannel, 0, len(broker.channels))
	for _, channel := range broker.channels {
		channels = append(channels, channel)
	}
	broker.mu.Unlock()
	for _, channel := range channels {
		_ = channel.conn.Close()
	}
}

// Close ends every live registration, so each run sees its claim drop and
// stops its Agent from opening uncaptured connections. Listeners are owned
// and closed by the caller.
func (broker *Broker) Close() {
	broker.stopOnce.Do(func() { close(broker.stop) })
	broker.revokeRegistrations()
}

type registrationRequest struct {
	Version int    `json:"v"`
	Run     string `json:"run"`
	// Token is the value the run set for TokenEnvironment in its Agent's
	// environment. It is optional; without it only lineage attributes.
	Token string `json:"token,omitempty"`
	// Bypass is the run's capture scope as addresses (ADR 0025). The broker
	// decides by address only; names are left to the run's ingress.
	Bypass []BypassAddress `json:"bypass,omitempty"`
}

// BypassAddress is one address rule of a run's capture scope.
type BypassAddress struct {
	Prefix string `json:"prefix"`
	Port   uint16 `json:"port,omitempty"`
}

// bypassScope compiles a run's address rules with the same bounds as the
// Environment's policy, plus the system DNS path the extension keeps.
func bypassScope(addresses []BypassAddress) (capturescope.Scope, error) {
	policy := capturescope.Policy{}
	for i, address := range addresses {
		policy.Rules = append(policy.Rules, capturescope.Rule{
			ID: "address-" + strconv.Itoa(i+1), Kind: capturescope.KindPrefix,
			Value: address.Prefix, Port: address.Port,
		})
	}
	if err := policy.Validate(); err != nil {
		return capturescope.Scope{}, err
	}
	return capturescope.Compile(&policy, nil, capturescope.Options{DNSOnSystemPath: true}), nil
}

type registrationReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Extension reports whether a platform capture layer announced itself
	// recently. Without one, descendants' flows are not being intercepted.
	Extension bool `json:"extension,omitempty"`
}

func (broker *Broker) extensionActive() bool {
	broker.mu.Lock()
	streams := broker.streams
	broker.mu.Unlock()
	if streams > 0 {
		return true
	}
	last := broker.lastHello.Load()
	return last != 0 && broker.now().Sub(time.Unix(0, last)) < extensionFreshness
}

// ServeRegistrations accepts run registrations until listener closes.
func (broker *Broker) ServeRegistrations(listener *net.UnixListener) error {
	return serve(listener, broker.register)
}

// ServeFlows accepts flow submissions on a Unix socket, admitting peers that
// allowFlowPeer accepts.
func (broker *Broker) ServeFlows(listener *net.UnixListener) error {
	return serve(listener, func(conn *net.UnixConn) {
		peer, err := peerOf(conn)
		if err != nil || !broker.allowFlowPeer(peer) {
			conn.Close()
			return
		}
		broker.flow(conn, nil)
	})
}

// ServeFlowsTCP accepts flow submissions on loopback TCP, where the sandboxed
// macOS extension (running as root) can reach a user-level vibermated. Each
// request must carry secret, which the app hands the extension through its
// provider configuration.
func (broker *Broker) ServeFlowsTCP(listener *net.TCPListener, secret []byte) error {
	if len(secret) < 32 {
		return errors.New("flow secret must be at least 32 bytes")
	}
	for {
		conn, err := listener.AcceptTCP()
		if err != nil {
			return err
		}
		go broker.flow(conn, secret)
	}
}

func serve(listener *net.UnixListener, handle func(*net.UnixConn)) error {
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return err
		}
		go handle(conn)
	}
}

// register binds the connecting process (from peer credentials, never from
// the request) as a lineage root. The registration lasts as long as the
// connection; owned flows are passed back over it.
func (broker *Broker) register(conn *net.UnixConn) {
	defer conn.Close()
	reply := func(err error) {
		message := registrationReply{OK: err == nil, Extension: err == nil && broker.extensionActive()}
		if err != nil {
			message.Error = err.Error()
		}
		encoded, _ := json.Marshal(message)
		_, _ = conn.Write(append(encoded, '\n'))
	}
	_ = conn.SetReadDeadline(time.Now().Add(requestTimeout))
	line, err := bufio.NewReaderSize(io.LimitReader(conn, maxRequestBytes), maxRequestBytes).ReadBytes('\n')
	if err != nil {
		reply(errors.New("registration request is invalid"))
		return
	}
	var request registrationRequest
	if json.Unmarshal(line, &request) != nil || request.Version != 1 || request.Run == "" || len(request.Run) > 256 ||
		(request.Token != "" && !ValidToken(request.Token)) {
		reply(errors.New("registration request is invalid"))
		return
	}
	scope, err := bypassScope(request.Bypass)
	if err != nil {
		reply(errors.New("registration capture scope is invalid"))
		return
	}
	peer, err := peerOf(conn)
	if err != nil {
		reply(err)
		return
	}
	root, err := broker.reader.Read(peer.PID)
	if err != nil {
		reply(errors.New("registering process is unavailable"))
		return
	}
	// Socket credentials prove the same process is registering again, for
	// example after it judged its previous claim lost. The new claim replaces
	// the old one instead of waiting for the broker to notice the old close.
	if previous, exists := broker.lineage.RootRun(root.Identity); exists {
		broker.mu.Lock()
		old := broker.channels[previous]
		delete(broker.channels, previous)
		broker.mu.Unlock()
		broker.lineage.Deregister(previous)
		if old != nil {
			_ = old.conn.Close()
		}
	}
	key := request.Run + "#" + strconv.FormatUint(broker.sequence.Add(1), 10)
	marker := ""
	if request.Token != "" {
		marker = TokenEnvironment + "=" + request.Token
	}
	if err := broker.lineage.RegisterWithMarker(root.Identity, key, marker); err != nil {
		reply(err)
		return
	}
	channel := &runChannel{conn: conn, scope: scope}
	broker.mu.Lock()
	broker.channels[key] = channel
	broker.mu.Unlock()
	defer func() {
		broker.lineage.Deregister(key)
		broker.mu.Lock()
		if broker.channels[key] == channel {
			delete(broker.channels, key)
		}
		broker.mu.Unlock()
	}()
	reply(nil)
	_ = conn.SetReadDeadline(time.Time{})
	stopKeepalive := make(chan struct{})
	defer close(stopKeepalive)
	go channel.keepalive(stopKeepalive, broker.interval)
	// The run sends nothing more; a read returns when it disconnects.
	_, _ = io.Copy(io.Discard, conn)
}

// keepalive lets the run tell a live broker from a hung one: a run that
// hears nothing for several intervals treats its claim as lost.
func (channel *runChannel) keepalive(stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			channel.mu.Lock()
			_, err := channel.conn.Write([]byte{keepaliveMarker})
			channel.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// flow answers whether the submitting process's flow is owned. An owned
// flow's connection is passed to its run, which reads the flow header and
// bytes the extension sends next. With a secret, the request must carry it.
func (broker *Broker) flow(conn fileConn, secret []byte) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(requestTimeout))
	request, err := readFlowRequest(conn)
	if err != nil {
		return
	}
	if secret != nil && subtle.ConstantTimeCompare([]byte(request.Secret), secret) != 1 {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if request.Hello {
		broker.lastHello.Store(broker.now().UnixNano())
		broker.queryFailures.Store(request.QueryFailures)
		if _, err := conn.Write([]byte{helloAccepted}); err != nil || !request.Stream {
			return
		}
		broker.mu.Lock()
		broker.streams++
		broker.mu.Unlock()
		broker.checkLiveness()
		// The extension sends nothing more; the read returns when it stops,
		// crashes or loses the connection.
		_, _ = io.Copy(io.Discard, conn)
		// A closed stream means the extension stopped or crashed: its last
		// hello no longer proves anything. A live extension's next periodic
		// hello restores activity.
		broker.lastHello.Store(0)
		broker.mu.Lock()
		broker.streams--
		broker.mu.Unlock()
		broker.checkLiveness()
		return
	}
	key, owned := broker.lineage.Owner(request.PID)
	broker.mu.Lock()
	channel := broker.channels[key]
	broker.mu.Unlock()
	if !owned || channel == nil {
		_, _ = conn.Write([]byte{flowUnowned})
		return
	}
	if destination, err := netip.ParseAddrPort(request.Destination); err == nil {
		if _, bypass := channel.scope.Match(capturescope.Destination{
			Addr: destination.Addr(), Port: destination.Port(),
		}); bypass {
			_, _ = conn.Write([]byte{flowDirect})
			return
		}
	}
	if _, err := conn.Write([]byte{flowOwned}); err != nil {
		return
	}
	channel.mu.Lock()
	_ = passConn(channel.conn, conn)
	channel.mu.Unlock()
}
