//go:build linux || darwin

package flowbroker

import (
	"bufio"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vibe-agi/vibermate/internal/processlineage"
)

const (
	maxRequestBytes = 512
	requestTimeout  = 2 * time.Second
	flowOwned       = 'Y'
	flowUnowned     = 'N'
)

// Broker attributes intercepted flows to Capture Runs and hands each owned
// flow's connection to the run that registered its process lineage.
type Broker struct {
	lineage *processlineage.Registry
	reader  processlineage.Reader
	// allowFlowPeer decides which Unix-socket peers may submit flows, so a
	// same-user process cannot route traffic under another run's identity.
	// Loopback TCP submissions authenticate with a secret instead.
	allowFlowPeer func(Peer) bool

	sequence atomic.Uint64
	mu       sync.Mutex
	channels map[string]*runChannel
}

type runChannel struct {
	mu   sync.Mutex
	conn *net.UnixConn
}

func New(reader processlineage.Reader, allowFlowPeer func(Peer) bool) (*Broker, error) {
	if reader == nil || allowFlowPeer == nil {
		return nil, errors.New("flow broker reader and flow peer policy are required")
	}
	return &Broker{
		lineage:       processlineage.NewRegistry(reader),
		reader:        reader,
		allowFlowPeer: allowFlowPeer,
		channels:      map[string]*runChannel{},
	}, nil
}

type registrationRequest struct {
	Version int    `json:"v"`
	Run     string `json:"run"`
}

type registrationReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
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
		message := registrationReply{OK: err == nil}
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
	if json.Unmarshal(line, &request) != nil || request.Version != 1 || request.Run == "" || len(request.Run) > 256 {
		reply(errors.New("registration request is invalid"))
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
	key := request.Run + "#" + strconv.FormatUint(broker.sequence.Add(1), 10)
	if err := broker.lineage.Register(root.Identity, key); err != nil {
		reply(err)
		return
	}
	channel := &runChannel{conn: conn}
	broker.mu.Lock()
	broker.channels[key] = channel
	broker.mu.Unlock()
	defer func() {
		broker.lineage.Deregister(key)
		broker.mu.Lock()
		delete(broker.channels, key)
		broker.mu.Unlock()
	}()
	reply(nil)
	_ = conn.SetReadDeadline(time.Time{})
	// The run sends nothing more; a read returns when it disconnects.
	_, _ = io.Copy(io.Discard, conn)
}

type flowRequest struct {
	Version int    `json:"v"`
	PID     int    `json:"pid"`
	Secret  string `json:"secret,omitempty"`
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
	key, owned := broker.lineage.Owner(request.PID)
	broker.mu.Lock()
	channel := broker.channels[key]
	broker.mu.Unlock()
	if !owned || channel == nil {
		_, _ = conn.Write([]byte{flowUnowned})
		return
	}
	if _, err := conn.Write([]byte{flowOwned}); err != nil {
		return
	}
	channel.mu.Lock()
	_ = passConn(channel.conn, conn)
	channel.mu.Unlock()
}

func readFlowRequest(r io.Reader) (flowRequest, error) {
	var size [2]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return flowRequest{}, err
	}
	n := int(binary.BigEndian.Uint16(size[:]))
	if n == 0 || n > maxRequestBytes {
		return flowRequest{}, errors.New("flow request is invalid")
	}
	encoded := make([]byte, n)
	if _, err := io.ReadFull(r, encoded); err != nil {
		return flowRequest{}, err
	}
	var request flowRequest
	if err := json.Unmarshal(encoded, &request); err != nil || request.Version != 1 || request.PID <= 1 {
		return flowRequest{}, fmt.Errorf("flow request is invalid")
	}
	return request, nil
}

// WriteFlowRequest frames a flow ownership query, as the extension sends it.
// secret is empty on a Unix socket, where peer credentials authenticate.
func WriteFlowRequest(w io.Writer, pid int, secret string) error {
	encoded, err := json.Marshal(flowRequest{Version: 1, PID: pid, Secret: secret})
	if err != nil {
		return err
	}
	frame := make([]byte, 2+len(encoded))
	binary.BigEndian.PutUint16(frame, uint16(len(encoded)))
	copy(frame[2:], encoded)
	_, err = w.Write(frame)
	return err
}
