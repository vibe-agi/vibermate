package productruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

type runtimeResumeProber struct {
	provider offlinehold.Prober
	original offlinehold.Prober
}

func newRuntimeResumeProber(
	provider offlinehold.Prober,
	original offlinehold.Prober,
) (*runtimeResumeProber, error) {
	if provider == nil || original == nil {
		return nil, errors.New("runtime resume probe dependencies are incomplete")
	}
	return &runtimeResumeProber{
		provider: provider,
		original: original,
	}, nil
}

func (prober *runtimeResumeProber) Probe(
	ctx context.Context,
	request offlinehold.ProbeRequest,
) error {
	if prober == nil ||
		prober.provider == nil ||
		prober.original == nil ||
		ctx == nil ||
		len(request.Targets) == 0 {
		return offlinehold.NewProbeFailure(
			offlinehold.ProbeReasonFailed,
			errors.New("runtime resume probe request is invalid"),
		)
	}
	for _, target := range request.Targets {
		var selected offlinehold.Prober
		switch target.Kind {
		case offlinehold.EgressProvider:
			selected = prober.provider
		case offlinehold.EgressOpaque, offlinehold.EgressAuxiliary:
			selected = prober.original
		default:
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("runtime resume probe target kind is unsupported"),
			)
		}
		if err := selected.Probe(
			ctx,
			offlinehold.ProbeRequest{
				Targets: []offlinehold.ProbeTarget{target},
			},
		); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) OfflineHoldSnapshot() offlinehold.Snapshot {
	return r.offlineHold.Snapshot()
}

// EnterOfflineHold closes new external egress admission at the requested
// runtime-local revision.
func (r *Runtime) EnterOfflineHold(
	ctx context.Context,
	expectedRevision uint64,
) (offlinehold.Snapshot, error) {
	return r.offlineHold.Enter(ctx, expectedRevision)
}

// ResumeOfflineHold probes exactly the frozen targets of currently queued
// requests. Callers cannot supply a different route, and normal egress
// admission remains closed throughout the probe.
func (r *Runtime) ResumeOfflineHold(
	ctx context.Context,
	expectedRevision uint64,
) (offlinehold.Snapshot, error) {
	targets, err := r.resumeProbeTargets()
	if err != nil {
		return r.offlineHold.Snapshot(), err
	}
	return r.offlineHold.Resume(
		ctx,
		expectedRevision,
		offlinehold.ResumeRequest{Targets: targets},
		r.resumeProber,
	)
}

func (r *Runtime) resumeProbeTargets() ([]offlinehold.ProbeTarget, error) {
	if r == nil || r.offlineHold == nil || r.probeCatalog == nil {
		return nil, errors.New("runtime resume target dependencies are incomplete")
	}
	targets := r.offlineHold.PendingProbeTargets()
	providerTargets, err := r.probeCatalog.ActiveProviderProbeTargets()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(targets)+len(providerTargets))
	normalized := make(
		[]offlinehold.ProbeTarget,
		0,
		len(targets)+len(providerTargets),
	)
	appendUnique := func(target offlinehold.ProbeTarget) {
		key := probeTargetIdentityKey(target)
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		normalized = append(normalized, target)
	}
	for _, target := range targets {
		appendUnique(target)
	}
	for _, candidate := range providerTargets {
		target, targetErr := providertransport.NewTarget(candidate.Target())
		if targetErr != nil {
			return nil, targetErr
		}
		frozen, freezeErr := providertransport.NewProbeTarget(
			candidate.Reference(),
			candidate.AccessRevision(),
			candidate.PlanHash(),
			target,
		)
		if freezeErr != nil {
			return nil, freezeErr
		}
		appendUnique(frozen)
	}
	slices.SortFunc(normalized, func(left, right offlinehold.ProbeTarget) int {
		leftKey := probeTargetIdentityKey(left)
		rightKey := probeTargetIdentityKey(right)
		return strings.Compare(leftKey, rightKey)
	})
	return normalized, nil
}

func probeTargetIdentityKey(target offlinehold.ProbeTarget) string {
	return strings.Join([]string{
		string(target.Kind),
		string(target.Transport),
		target.TargetRef,
		target.NetworkOrigin,
		target.HTTPAuthority,
		target.TLSServerName,
		fmt.Sprintf("%020d", target.AccessRevision),
		target.PlanHash,
	}, "\x00")
}

var _ offlinehold.Prober = (*runtimeResumeProber)(nil)
