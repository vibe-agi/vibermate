package productruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

type runtimeResumeProber struct {
	provider offlinehold.Prober
	original offlinehold.Prober
	blind    offlinehold.Prober
}

func newRuntimeResumeProber(
	provider offlinehold.Prober,
	original offlinehold.Prober,
	blind offlinehold.Prober,
) (*runtimeResumeProber, error) {
	if provider == nil || original == nil || blind == nil {
		return nil, errors.New("runtime resume probe dependencies are incomplete")
	}
	return &runtimeResumeProber{
		provider: provider,
		original: original,
		blind:    blind,
	}, nil
}

func (prober *runtimeResumeProber) Probe(
	ctx context.Context,
	request offlinehold.ProbeRequest,
) error {
	if prober == nil ||
		prober.provider == nil ||
		prober.original == nil ||
		prober.blind == nil ||
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
		case offlinehold.EgressBlindTunnel:
			// A CONNECT that arrived after Enter is queued like anything else,
			// so resume has to clear it. Having no arm here meant one tunnel
			// failed the whole probe set and released nothing — including
			// every provider request waiting behind it.
			selected = prober.blind
		default:
			// Plugin distribution and update egress have Hold kinds but no
			// subsystem that can queue one yet, so reaching here is a
			// programming error rather than a network condition. Saying that
			// is more useful than a reason code a person would read as "the
			// network is down".
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				fmt.Errorf(
					"runtime resume has no prober for egress kind %q",
					target.Kind,
				),
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
	if r == nil || r.offlineHold == nil {
		return nil, errors.New("runtime resume target dependencies are incomplete")
	}
	targets := r.offlineHold.PendingProbeTargets()
	seen := make(map[string]struct{}, len(targets))
	normalized := make(
		[]offlinehold.ProbeTarget,
		0,
		len(targets),
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
		fmt.Sprintf("%020d", target.PlanRevision),
		target.PlanDigest,
	}, "\x00")
}

var _ offlinehold.Prober = (*runtimeResumeProber)(nil)
