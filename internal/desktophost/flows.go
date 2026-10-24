package desktophost

import (
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"sync"

	"github.com/vibe-agi/vibermate/internal/flowbroker"
	"github.com/vibe-agi/vibermate/internal/processlineage"
)

// flowService owns transparent-capture rendezvous for one generation: the
// Unix socket where vibermate run registers, and the loopback TCP listener
// where the macOS extension submits flows with the generation's secret.
type flowService struct {
	broker       *flowBroker
	registration *net.UnixListener
	flows        *net.TCPListener
	socketPath   string
	secret       string
	serving      sync.WaitGroup
	closeOnce    sync.Once
}

type flowBroker = flowbroker.Broker

func startFlowService(socketPath string, random io.Reader) (*flowService, error) {
	secret, err := randomCapability(random)
	if err != nil {
		return nil, err
	}
	// The generation lock is held, so a socket left at this path belongs to a
	// generation that is gone.
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode().Type() != fs.ModeSocket {
			return nil, errors.New("flow registration path is not a socket")
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, err
		}
	}
	broker, err := flowbroker.New(processlineage.System(), func(flowbroker.Peer) bool { return false })
	if err != nil {
		return nil, err
	}
	registration, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, err
	}
	registration.SetUnlinkOnClose(true)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		registration.Close()
		return nil, err
	}
	flows, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		registration.Close()
		return nil, err
	}
	service := &flowService{
		broker: broker, registration: registration, flows: flows,
		socketPath: socketPath, secret: secret,
	}
	service.serving.Add(2)
	go func() { defer service.serving.Done(); _ = broker.ServeRegistrations(registration) }()
	go func() { defer service.serving.Done(); _ = broker.ServeFlowsTCP(flows, []byte(secret)) }()
	return service, nil
}

func (service *flowService) portOrZero() int {
	if service == nil {
		return 0
	}
	return service.flows.Addr().(*net.TCPAddr).Port
}

func (service *flowService) secretOrEmpty() string {
	if service == nil {
		return ""
	}
	return service.secret
}

// Close stops admitting flows and registrations, then ends live claims so each
// run pauses its Agent instead of letting new connections leave uncaptured.
func (service *flowService) Close() error {
	var err error
	service.closeOnce.Do(func() {
		err = errors.Join(normalizeClose(service.flows.Close()), normalizeClose(service.registration.Close()))
		service.serving.Wait()
		service.broker.Close()
	})
	return err
}
