//go:build darwin

package desktoptrust

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/systemtrust"
)

func installClientCertificate(ctx context.Context, path, fingerprint string) error {
	executor, err := NewProductionCommandExecutor()
	if err != nil {
		return err
	}
	return systemtrust.InstallImportedCertificate(ctx, executor, path, fingerprint)
}
