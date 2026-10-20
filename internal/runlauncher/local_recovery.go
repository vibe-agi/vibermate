package runlauncher

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
)

// localRecoverySession belongs to one launched process, not to the App. Only
// its supervisor goroutine changes control/grant. The caller joins that
// goroutine before finalization, so it always finishes the current grant.
type localRecoverySession struct {
	launcher         *Launcher
	create           capturecontrol.CreateRequest
	request          LaunchRequest
	control          *controlClient
	grant            capturecontrol.LaunchGrant
	initial          capturecontrol.LaunchGrant
	frontier         *localProxyFrontier
	executable       [sha256.Size]byte
	root             [sha256.Size]byte
	childEnvironment []string
	done             chan struct{}
}

func (launcher *Launcher) newLocalRecovery(ctx context.Context, control *controlClient, grant capturecontrol.LaunchGrant, create capturecontrol.CreateRequest, request LaunchRequest) (*localRecoverySession, error) {
	if grant.LocalRecovery == nil {
		return nil, nil
	}
	executable, err := localExecutableFingerprint(ctx, grant.ExecutablePath)
	if err != nil {
		return nil, err
	}
	root, err := localRootFingerprint(grant)
	if err != nil {
		return nil, err
	}
	frontier, err := startLocalProxyFrontier(localProxyTarget{Generation: control.generation, ProxyOrigin: grant.ProxyAddress, ProxyToken: grant.ProxyToken})
	if err != nil {
		return nil, err
	}
	return &localRecoverySession{
		launcher: launcher, create: create, request: request, control: control, grant: grant, initial: grant,
		frontier: frontier, executable: executable, root: root,
	}, nil
}

func (session *localRecoverySession) childGrant(grant capturecontrol.LaunchGrant) capturecontrol.LaunchGrant {
	grant.ProxyAddress, grant.ProxyToken = session.frontier.Origin(), session.frontier.Token()
	return grant
}

func (session *localRecoverySession) close() {
	session.frontier.Close()
	session.control.close()
}

func (session *localRecoverySession) heartbeat(ctx context.Context, pid int) <-chan error {
	failure := make(chan error, 1)
	session.done = make(chan struct{})
	go func() {
		defer close(session.done)
		timer := time.NewTimer(session.launcher.config.HeartbeatInterval)
		defer timer.Stop()
		interrupted := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			err := session.launcher.callWithTimeout(ctx, func(call context.Context) error { return session.control.heartbeat(call, session.grant) })
			if ctx.Err() != nil {
				return
			}
			// An abruptly stopped App can leave a durable lease that a new
			// generation accepts at the same address. Even a 200 heartbeat
			// therefore cannot substitute for checking fresh discovery.
			_, err = session.recover(ctx, pid, err)
			if ctx.Err() != nil {
				return
			}
			if retryableControlFailure(err) {
				// Control-plane latency must not cut an authorized model stream.
				// The Runtime still enforces the current proxy lease itself.
				if !interrupted {
					session.launcher.writeHeartbeatDiagnostic("vibermate: Desktop App connection interrupted; keeping the command running and waiting to reconnect. Open the App to restore proxy access.")
					interrupted = true
				}
				timer.Reset(min(session.launcher.config.HeartbeatInterval, 2*time.Second))
				continue
			}
			if err != nil {
				session.frontier.Withdraw()
				session.launcher.writeHeartbeatDiagnostic("vibermate: Desktop reconnection was rejected or the launch policy changed. Start the command again to apply current authorization and client settings.")
				failure <- err
				return
			}
			if interrupted {
				session.launcher.writeHeartbeatDiagnostic("vibermate: Desktop App connection restored; the existing command can continue.")
				interrupted = false
			}
			timer.Reset(session.launcher.config.HeartbeatInterval)
		}
	}()
	return failure
}

func (session *localRecoverySession) recover(ctx context.Context, pid int, heartbeatErr error) (bool, error) {
	discovery, err := session.launcher.config.Discovery.Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, localdiscovery.ErrExpired) {
			// Graceful shutdown revokes leases before removing discovery and
			// draining the listeners. A rejection from that retired endpoint
			// cannot decide the lifetime of this still-running child.
			return false, localRecoveryUnavailable()
		}
		return false, fmt.Errorf("reload local Runtime discovery: %w", err)
	}
	if discovery.InstanceID == session.control.generation {
		if discovery.BaseURL != session.control.origin || discovery.ControlCredential != session.control.credential {
			return false, errors.New("local Runtime discovery changed within the same generation")
		}
		if heartbeatErr != nil && !retryableControlFailure(heartbeatErr) {
			var status RuntimeInspection
			err := session.launcher.callWithTimeout(ctx, func(call context.Context) error {
				var inspectErr error
				status, inspectErr = session.control.inspectLocal(call, discovery)
				return inspectErr
			})
			if err != nil {
				return false, err
			}
			if !status.Ready {
				return false, localRecoveryUnavailable()
			}
		}
		return false, heartbeatErr
	}
	session.frontier.Withdraw()
	next, err := newControlClient(discovery, max(controlTransportTimeout(session.launcher.config), session.launcher.initialControlTimeout))
	if err != nil {
		return false, err
	}
	accepted := false
	defer func() {
		if !accepted {
			next.close()
		}
	}()
	var status RuntimeInspection
	err = session.launcher.callWithin(ctx, session.launcher.initialControlTimeout, func(call context.Context) error {
		var inspectErr error
		status, inspectErr = next.inspectLocal(call, discovery)
		return inspectErr
	})
	if err != nil {
		return false, err
	}
	if status.Host != "desktop" {
		return false, errors.New("local recovery requires the Desktop Runtime")
	}
	if !status.Ready {
		return false, localRecoveryUnavailable()
	}
	var grant capturecontrol.LaunchGrant
	err = session.launcher.callWithin(ctx, session.launcher.config.CreateTimeout, func(call context.Context) error {
		var createErr error
		grant, createErr = next.createLocal(call, session.create)
		return createErr
	})
	if err != nil {
		return false, err
	}
	defer func() {
		if !accepted {
			session.launcher.finishBestEffort(next, grant)
		}
	}()
	if err := session.validateReplacement(ctx, grant); err != nil {
		return false, err
	}
	if runtime.GOOS == "darwin" && grant.LaunchRecipe == clientadapter.LaunchCodexResponsesHTTP {
		if err := session.launcher.callWithTimeout(ctx, next.checkLocalRootTrust); err != nil {
			return false, err
		}
	}
	if err := session.launcher.callWithin(ctx, session.launcher.initialControlTimeout, func(call context.Context) error { return next.attachRecovered(call, grant, pid) }); err != nil {
		return false, err
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err := session.frontier.ReplaceTarget(localProxyTarget{Generation: discovery.InstanceID, ProxyOrigin: grant.ProxyAddress, ProxyToken: grant.ProxyToken}); err != nil {
		return false, err
	}
	previousControl := session.control
	session.control, session.grant = next, grant
	accepted = true
	previousControl.close()
	session.launcher.writeHeartbeatDiagnostic("vibermate: continuing the same process with a fresh Capture after Desktop restart. New requests appear in that Capture; tool approvals must be granted again.")
	return true, nil
}

func localRecoveryUnavailable() error {
	return &ControlFailure{Status: 503, ReasonCode: capturecontrol.ReasonRunControlUnavailable, Stage: "session_check"}
}

func (session *localRecoverySession) validateReplacement(ctx context.Context, next capturecontrol.LaunchGrant) error {
	if err := validateGrant(next); err != nil {
		return err
	}
	initial := session.initial
	if next.LocalRecovery == nil || !reflect.DeepEqual(initial.LocalRecovery, next.LocalRecovery) ||
		next.Run.ID == initial.Run.ID || next.ProxyToken == session.grant.ProxyToken || next.RunCapability == session.grant.RunCapability ||
		next.ExecutablePath != initial.ExecutablePath || next.LaunchRecipe != initial.LaunchRecipe || next.Recognition != initial.Recognition ||
		next.ProxyDelivery != capturecontrol.ProxyDeliveryLocalListener ||
		!reflect.DeepEqual(initial.NativeToolLogin, next.NativeToolLogin) ||
		!reflect.DeepEqual(initial.LaunchEnvironment, next.LaunchEnvironment) ||
		!reflect.DeepEqual(initial.ProtectedAuthorities, next.ProtectedAuthorities) ||
		!reflect.DeepEqual(initial.ManagedCredentialAuthorities, next.ManagedCredentialAuthorities) {
		return errors.New("local Runtime restart changed the immutable launch policy; restart the command to apply it")
	}
	// Catalog publication may change during an App upgrade. The executable
	// bytes and every behavior delivered to this child must remain identical.
	if !sameLocalClientBehavior(initial, next) {
		return errors.New("local Runtime restart changed client launch behavior")
	}
	executable, err := localExecutableFingerprint(ctx, next.ExecutablePath)
	if err != nil || executable != session.executable {
		return errors.New("captured executable changed during Desktop restart")
	}
	root, err := localRootFingerprint(next)
	if err != nil || root != session.root {
		return errors.New("Desktop Root certificate changed; restart the command with the current Root")
	}
	childGrant := session.childGrant(next)
	childGrant.Run.ID = initial.Run.ID // The child retains its initial launch tag.
	childGrant.RootPEMPath = initial.RootPEMPath
	childEnvironment, err := buildEnvironment(session.launcher.config.BaseEnvironment, childGrant)
	if err != nil {
		return err
	}
	if session.request.ManagedAuth {
		_, childEnvironment, err = prepareManagedAuth(childGrant, session.request.EnvironmentID, session.request.Command[1:], session.launcher.config.BaseEnvironment, childEnvironment)
		if err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(session.childEnvironment, childEnvironment) {
		return errors.New("Desktop restart changed the running command's environment")
	}
	return nil
}

func sameLocalClientBehavior(initial, next capturecontrol.LaunchGrant) bool {
	if (initial.Adapter == nil) != (next.Adapter == nil) || (initial.Signer == nil) != (next.Signer == nil) {
		return false
	}
	if initial.Adapter != nil {
		a, b := *initial.Adapter, *next.Adapter
		a.CatalogRevision, b.CatalogRevision = 0, 0
		if a != b {
			return false
		}
	}
	if initial.Signer != nil {
		a, b := *initial.Signer, *next.Signer
		a.CatalogRevision, b.CatalogRevision = 0, 0
		if a != b {
			return false
		}
	}
	return true
}

func localExecutableFingerprint(ctx context.Context, path string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return result, errors.New("inspect captured executable for restart recovery")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return result, errors.New("captured executable is not a regular file")
	}
	hash := sha256.New()
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		n, readErr := file.Read(buffer)
		if n != 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return result, errors.New("read captured executable for restart recovery")
		}
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return result, errors.New("captured executable changed while verifying restart recovery")
	}
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func localRootFingerprint(grant capturecontrol.LaunchGrant) ([sha256.Size]byte, error) {
	if !grant.LaunchRecipe.RequiresRoot() {
		return [sha256.Size]byte{}, nil
	}
	// Public certificate only; never read the Root key or client credentials.
	file, err := os.Open(grant.RootPEMPath)
	if err != nil {
		return [sha256.Size]byte{}, errors.New("read public Desktop Root for restart recovery")
	}
	defer file.Close()
	certificate, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(certificate) > 64<<10 || len(certificate) == 0 {
		return [sha256.Size]byte{}, errors.New("public Desktop Root is invalid")
	}
	return sha256.Sum256(certificate), nil
}
