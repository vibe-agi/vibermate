//go:build !darwin && !linux

package runlauncher

import (
	"context"
	"errors"
)

func (launcher *Launcher) RunACP(context.Context, ACPLaunchRequest) (int, error) {
	return 1, errors.New("ACP process supervision is currently supported on macOS and Linux")
}
