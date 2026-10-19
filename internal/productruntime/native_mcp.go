package productruntime

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureauthority"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/socksproxy"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
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
	approvals toolapproval.CaptureCanceler
	mu        sync.RWMutex
	executor  nativemcp.Executor
	socks     *socksproxy.Server
	retained  *captureadmission.Retained
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
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	switch nativemcp.RevocationReason(reason) {
	case nativemcp.RevocationPermissionOff, nativemcp.RevocationCaptureStopped, nativemcp.RevocationAssignmentApplied, nativemcp.RevocationOwnerChanged, nativemcp.RevocationShutdown:
	default:
		return nativetool.ErrInvalid
	}
	adapter.mu.RLock()
	executor := adapter.executor
	socks := adapter.socks
	retained := adapter.retained
	approvals := adapter.approvals
	adapter.mu.RUnlock()
	var errs []error
	var waitSOCKS func(context.Context) error
	var waitRetained func(context.Context) error
	if retained != nil {
		waitRetained = retained.BeginChange(captureauthority.Change{Capture: capture})
	}
	if socks != nil {
		var err error
		waitSOCKS, err = socks.BeginRevoke(capture)
		errs = append(errs, err)
	}
	if approvals != nil {
		errs = append(errs, approvals.CancelCaptureApprovals(ctx, capture, reason))
	}
	if executor != nil {
		errs = append(errs, executor.Revoke(ctx, capture, nativemcp.RevocationReason(reason)))
	}
	if waitRetained != nil {
		errs = append(errs, waitRetained(ctx))
	}
	if waitSOCKS != nil {
		errs = append(errs, waitSOCKS(ctx))
	}
	return errors.Join(errs...)
}

func (adapter *nativeRevocation) installRetained(retained *captureadmission.Retained) {
	adapter.mu.Lock()
	adapter.retained = retained
	adapter.mu.Unlock()
}
func (adapter *nativeRevocation) Changed(ctx context.Context, change captureauthority.Change) error {
	adapter.mu.RLock()
	retained := adapter.retained
	adapter.mu.RUnlock()
	if retained == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return retained.Changed(ctx, change)
}

func (adapter *nativeRevocation) installApprovals(owner toolapproval.CaptureCanceler) {
	adapter.mu.Lock()
	adapter.approvals = owner
	adapter.mu.Unlock()
}
