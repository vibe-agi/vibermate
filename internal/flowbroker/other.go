//go:build !linux && !darwin

package flowbroker

import (
	"errors"
	"net"
)

var errUnsupported = errors.New("transparent capture is unsupported on this platform")

type Registration struct{}

func Register(string, string, func(net.Conn)) (*Registration, error) { return nil, errUnsupported }

func (*Registration) Close() error { return nil }
