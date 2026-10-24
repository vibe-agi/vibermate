//go:build !linux && !darwin

package flowbroker

import (
	"errors"
	"net"

	"github.com/vibe-agi/vibermate/internal/processlineage"
)

var errUnsupported = errors.New("transparent capture is unsupported on this platform")

type Broker struct{}

func New(processlineage.Reader, func(Peer) bool) (*Broker, error) { return nil, errUnsupported }

func (*Broker) ServeRegistrations(*net.UnixListener) error   { return errUnsupported }
func (*Broker) ServeFlowsTCP(*net.TCPListener, []byte) error { return errUnsupported }
func (*Broker) Close()                                       {}

type Registration struct{}

func Register(string, string, func(net.Conn)) (*Registration, error) { return nil, errUnsupported }

func (*Registration) Close() error { return nil }

func (*Registration) Done() <-chan struct{} { return nil }
