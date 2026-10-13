package productruntime

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"sync"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/socksproxy"
)

type nativeIDs struct{ random io.Reader }

func (ids nativeIDs) NewNativeOperationID(ctx context.Context) (nativetool.ID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var data [16]byte
	if _, err := io.ReadFull(ids.random, data[:]); err != nil {
		return "", err
	}
	return nativetool.NewID(hex.EncodeToString(data[:]))
}
func (ids nativeIDs) NewOwnerEpoch(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var data [8]byte
	if _, err := io.ReadFull(ids.random, data[:]); err != nil {
		return 0, err
	}
	epoch := binary.BigEndian.Uint64(data[:]) & uint64(1<<63-1)
	if epoch == 0 {
		return 0, errors.New("native owner epoch unavailable")
	}
	return epoch, nil
}

// This adapter exists before assignments recover; listeners start only after
// the actual module has recovered the same Store and is installed here.
type nativeRevocation struct {
	mu       sync.RWMutex
	executor nativemcp.Executor
	socks    *socksproxy.Server
}

func (adapter *nativeRevocation) installSOCKS(server *socksproxy.Server) {
	adapter.mu.Lock()
	adapter.socks = server
	adapter.mu.Unlock()
}

func (adapter *nativeRevocation) install(executor nativemcp.Executor) {
	adapter.mu.Lock()
	adapter.executor = executor
	adapter.mu.Unlock()
}
func (adapter *nativeRevocation) Revoke(ctx context.Context, capture captureidentity.Reference, reason string) error {
	switch nativemcp.RevocationReason(reason) {
	case nativemcp.RevocationPermissionOff, nativemcp.RevocationCaptureStopped, nativemcp.RevocationAssignmentApplied, nativemcp.RevocationOwnerChanged, nativemcp.RevocationShutdown:
	default:
		return nativetool.ErrInvalid
	}
	adapter.mu.RLock()
	executor := adapter.executor
	socks := adapter.socks
	adapter.mu.RUnlock()
	var errs []error
	var waitSOCKS func(context.Context) error
	if socks != nil {
		var err error
		waitSOCKS, err = socks.BeginRevoke(capture)
		errs = append(errs, err)
	}
	if executor != nil {
		errs = append(errs, executor.Revoke(ctx, capture, nativemcp.RevocationReason(reason)))
	}
	if waitSOCKS != nil {
		errs = append(errs, waitSOCKS(ctx))
	}
	return errors.Join(errs...)
}
