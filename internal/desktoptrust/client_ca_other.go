//go:build !darwin && !linux

package desktoptrust

import (
	"context"
	"errors"
)

func installClientCertificate(context.Context, string, string) error {
	return errors.New("CA installation is supported on macOS and supported Linux distributions; use ca download elsewhere")
}
