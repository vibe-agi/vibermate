//go:build !linux

package netnscapture

import (
	"errors"
	"os/exec"

	"github.com/vibe-agi/vibermate/internal/flowingress"
)

// ErrUnsupported reports a platform without namespace capture.
var ErrUnsupported = errors.New("namespace capture is available on Linux only")

type Session struct{}

func Prepare(*exec.Cmd, *flowingress.Ingress) (*Session, error) { return nil, ErrUnsupported }
func (*Session) Start() error                                   { return ErrUnsupported }
func (*Session) Close()                                         {}
func RunChild([]string) error                                   { return ErrUnsupported }
