package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func runAcceptance(
	parent context.Context,
	config config,
) (report acceptanceReport, resultErr error) {
	report = newReport(time.Now())
	defer func() {
		report.FinishedAt = time.Now().UTC()
	}()
	ctx, cancel := context.WithTimeout(parent, config.timeout)
	defer cancel()
	fail := func(id string, err error) (acceptanceReport, error) {
		report.add(id, checkFailed, "check failed")
		return report, fmt.Errorf("%s: %w", id, err)
	}
	provenance, provenanceErr := collectAcceptanceProvenance(ctx, config)
	report.Provenance = &provenance
	if provenanceErr != nil {
		return fail("build-provenance", provenanceErr)
	}
	if err := validateFrozenProvenance(provenance); err != nil {
		return fail("build-provenance", err)
	}
	report.add(
		"build-provenance",
		checkPassed,
		"App bundle and acceptance artifacts share one clean Git source identity",
	)
	phases, isolateErr := splitAcceptancePhases(config)
	if isolateErr != nil {
		return fail("deterministic-secret-reference", isolateErr)
	}
	config = phases.deterministic

	if err := verifyFixedClaude(ctx, config); err != nil {
		return fail("fixed-claude-identity", err)
	}
	report.add(
		"fixed-claude-identity",
		checkPassed,
		"Claude Code 2.1.220 executable digest matched",
	)
	layout, err := runtimepath.Default()
	if err != nil {
		return fail("runtime-layout", err)
	}
	guard, err := instanceguard.Acquire(layout.GenerationLock)
	if err != nil {
		return fail("exclusive-generation-preflight", err)
	}
	if err := guard.Release(); err != nil {
		return fail("exclusive-generation-preflight", err)
	}
	report.add(
		"exclusive-generation-preflight",
		checkPassed,
		"no other Desktop generation owned the runtime",
	)
	desktopHome, err := os.MkdirTemp("", "vibermate-desktop-shell-*")
	if err != nil {
		return fail("packaged-desktop-shell", err)
	}
	defer os.RemoveAll(desktopHome)
	if err := privateDirectory(desktopHome); err != nil {
		return fail("packaged-desktop-shell", err)
	}
	desktopLayout, err := runtimepath.FromAppCache(filepath.Join(
		desktopHome,
		"Library",
		"Caches",
		runtimepath.ApplicationID,
	))
	if err != nil {
		return fail("packaged-desktop-shell", err)
	}
	if err := exercisePackagedDesktopShell(
		ctx,
		config.desktopAppPath,
		desktopLayout,
		desktopHome,
	); err != nil {
		return fail("packaged-desktop-shell", err)
	}
	report.add(
		"packaged-desktop-shell",
		checkPassed,
		"packaged App used an isolated user directory, exchanged bootstrap, and gracefully drained its sidecar",
	)

	dataDirectory := config.dataDirectory
	temporaryData := false
	if dataDirectory == "" {
		dataDirectory, err = os.MkdirTemp("", "vibermate-assembly-*")
		if err != nil {
			return fail("private-data-directory", err)
		}
		temporaryData = true
	}
	if err := privateDirectory(dataDirectory); err != nil {
		return fail("private-data-directory", err)
	}
	if temporaryData && !config.keepData {
		defer cleanTemporaryData(dataDirectory)
	}
	report.add(
		"private-data-directory",
		checkPassed,
		"acceptance data directory is private",
	)

	first, err := startDaemon(
		ctx,
		config,
		layout.AppCacheDirectory,
		dataDirectory,
	)
	if err != nil {
		return fail("daemon-first-start", err)
	}
	firstStopped := false
	defer func() {
		if !firstStopped {
			stopContext, stopCancel := context.WithTimeout(
				context.Background(),
				10*time.Second,
			)
			_ = first.stopGracefully(stopContext)
			stopCancel()
		}
	}()
	if _, err := first.control.status(ctx); err != nil {
		return fail("daemon-first-start", err)
	}
	report.add(
		"daemon-first-start",
		checkPassed,
		"packaged daemon published a ready Desktop generation",
	)
	applied, status, problem, err := first.control.applyAccess(ctx, config, 0)
	if err != nil ||
		status != http.StatusOK ||
		problem.ReasonCode != "" ||
		applied.Outcome != access.WriteOutcomeCommitted ||
		applied.Revision != 1 ||
		len(applied.PlanHash) != 64 {
		return fail(
			"access-apply",
			fmt.Errorf(
				"status=%d reason=%s result=%+v err=%w",
				status,
				problem.ReasonCode,
				applied,
				err,
			),
		)
	}
	deterministicPlanHash := applied.PlanHash
	report.add(
		"access-apply",
		checkPassed,
		"revision 1 executable Access committed and published",
	)
	preflight, err := runHeldIngressPreflight(ctx, config, first)
	if err != nil {
		return fail("fixed-claude-opaque-control", err)
	}
	report.add(
		"fixed-claude-opaque-control",
		checkPassed,
		"fixed Claude resumed original-origin control traffic after an exact TLS probe; queuedKinds="+preflight.opaqueKinds,
	)
	report.add(
		"fixed-claude-provider-fail-closed",
		checkPassed,
		"the subsequent provider request failed before transport at the missing development credential boundary; reason="+preflight.providerReason,
	)
	connectionID, err := waitForProviderConnectionAudit(
		ctx,
		first.control,
		config,
		preflight.connectionBaseline,
		15*time.Second,
	)
	if err != nil {
		return fail("fixed-claude-connection-audit", err)
	}
	report.add(
		"fixed-claude-connection-audit",
		checkPassed,
		"durable MITM ConnectionEvent correlated the configured Agent ingress with the selected provider route; connectionId="+connectionID,
	)
	stopContext, stopCancel := context.WithTimeout(ctx, 30*time.Second)
	err = first.stopGracefully(stopContext)
	stopCancel()
	firstStopped = true
	if err != nil {
		return fail("daemon-sigint", err)
	}
	removed, err := discoveryRemoved(layout.LauncherRecord)
	if err != nil || !removed {
		return fail(
			"daemon-sigint",
			errors.New("graceful daemon exit left owned discovery"),
		)
	}
	report.add(
		"daemon-sigint",
		checkPassed,
		"SIGINT drained the Host and removed owned discovery",
	)

	second, err := startDaemon(
		ctx,
		config,
		layout.AppCacheDirectory,
		dataDirectory,
	)
	if err != nil {
		return fail("sqlite-reopen", err)
	}
	secondStopped := false
	defer func() {
		if !secondStopped {
			stopContext, stopCancel := context.WithTimeout(
				context.Background(),
				10*time.Second,
			)
			_ = second.stopGracefully(stopContext)
			stopCancel()
		}
	}()
	if second.descriptor.InstanceID == first.descriptor.InstanceID {
		return fail(
			"sqlite-reopen",
			errors.New("restarted daemon reused its runtime incarnation"),
		)
	}
	if err := requireRecoveredAccess(ctx, second.control, config); err != nil {
		return fail("sqlite-reopen", err)
	}
	report.add(
		"sqlite-reopen",
		checkPassed,
		"new incarnation recovered Access revision 1 from SQLite",
	)
	heldAgent, err := queueHeldIngressForDaemonKill(
		ctx,
		config,
		second,
	)
	if err != nil {
		return fail("daemon-sigkill-active-request", err)
	}
	heldRunFinished := false
	defer func() {
		if !heldRunFinished {
			_ = heldAgent.run.signalInterrupt()
			waitContext, cancelWait := context.WithTimeout(
				context.Background(),
				10*time.Second,
			)
			_, _ = heldAgent.run.wait(waitContext)
			cancelWait()
		}
		_ = os.RemoveAll(heldAgent.workingDirectory)
	}()
	killContext, killCancel := context.WithTimeout(ctx, 10*time.Second)
	secondStopped = true
	err = second.kill(killContext)
	killCancel()
	if err != nil {
		return fail("daemon-sigkill-active-request", err)
	}
	agentExitContext, cancelAgentExit := context.WithTimeout(
		ctx,
		45*time.Second,
	)
	_, err = heldAgent.run.wait(agentExitContext)
	cancelAgentExit()
	heldRunFinished = true
	if err != nil {
		return fail("daemon-sigkill-active-request", err)
	}
	_, _, _, markerSeen := heldAgent.run.evidence()
	if markerSeen {
		return fail(
			"daemon-sigkill-active-request",
			errors.New("killed held request produced a completion marker"),
		)
	}
	_ = os.RemoveAll(heldAgent.workingDirectory)
	report.add(
		"daemon-sigkill-active-request",
		checkPassed,
		"SIGKILL terminated a held fixed-Claude request without an upstream send or completion marker; queuedKinds="+heldAgent.queuedKinds,
	)

	third, err := startDaemon(
		ctx,
		config,
		layout.AppCacheDirectory,
		dataDirectory,
	)
	if err != nil {
		return fail("daemon-sigkill", err)
	}
	thirdStopped := false
	defer func() {
		if !thirdStopped {
			stopContext, stopCancel := context.WithTimeout(
				context.Background(),
				10*time.Second,
			)
			_ = third.stopGracefully(stopContext)
			stopCancel()
		}
	}()
	if third.descriptor.InstanceID == second.descriptor.InstanceID {
		return fail(
			"daemon-sigkill",
			errors.New("post-kill daemon reused its runtime incarnation"),
		)
	}
	if err := requireRecoveredAccess(ctx, third.control, config); err != nil {
		return fail("daemon-sigkill", err)
	}
	offlineSnapshot, err := third.control.offline(ctx)
	if err != nil {
		return fail("daemon-sigkill", err)
	}
	if err := requireSettledOfflineState(offlineSnapshot); err != nil {
		return fail("daemon-sigkill", err)
	}
	report.add(
		"daemon-sigkill",
		checkPassed,
		"post-kill generation recovered SQLite Access with no resurrected egress queue",
	)
	if err := requireRecoveredConnectionAudit(
		ctx,
		third.control,
		heldAgent.connectionID,
	); err != nil {
		return fail("daemon-sigkill-connection-recovery", err)
	}
	report.add(
		"daemon-sigkill-connection-recovery",
		checkPassed,
		"the restarted generation durably closed the interrupted ConnectionEvent with daemon_restarted",
	)

	if config.deterministicOnly {
		stopContext, stopCancel = context.WithTimeout(ctx, 30*time.Second)
		err = third.stopGracefully(stopContext)
		stopCancel()
		thirdStopped = true
		if err != nil {
			return fail("deterministic-shutdown", err)
		}
		report.add(
			"deterministic-shutdown",
			checkPassed,
			"deterministic acceptance generation drained",
		)
		return report, nil
	}
	credentialedApplied, credentialedStatus, credentialedProblem, credentialedErr :=
		third.control.applyAccess(ctx, phases.credentialed, 1)
	if credentialedErr != nil ||
		credentialedStatus != http.StatusOK ||
		credentialedProblem.ReasonCode != "" ||
		credentialedApplied.Outcome != access.WriteOutcomeCommitted ||
		credentialedApplied.Revision != 2 ||
		len(credentialedApplied.PlanHash) != 64 ||
		credentialedApplied.PlanHash == deterministicPlanHash {
		return fail(
			"credentialed-access-apply",
			fmt.Errorf(
				"status=%d reason=%s result=%+v err=%w",
				credentialedStatus,
				credentialedProblem.ReasonCode,
				credentialedApplied,
				credentialedErr,
			),
		)
	}
	config = phases.credentialed
	report.add(
		"credentialed-access-apply",
		checkPassed,
		"configured SecretRef rebound the Access as revision 2 without exposing its value",
	)
	credential, err := third.control.credential(ctx, config)
	if err != nil ||
		credential.SecretState != secretstore.StateConfigured ||
		credential.SecretRevision == 0 {
		report.add(
			"provider-secret",
			checkBlocked,
			"active credential is missing or unreadable by the current App identity",
		)
		return report, &blockedError{
			reason: "save the active provider credential through the current VibeMate App",
		}
	}
	report.add(
		"provider-secret",
		checkPassed,
		"active credential metadata is configured without reading its value",
	)

	if err := runNormalReply(ctx, config, third); err != nil {
		return fail("normal-streaming-reply", err)
	}
	report.add(
		"normal-streaming-reply",
		checkPassed,
		"fixed Claude completed an unheld streamed provider reply",
	)
	if err := runHeldStreaming(ctx, config, third); err != nil {
		return fail("planned-hold-streaming", err)
	}
	report.add(
		"planned-hold-streaming",
		checkPassed,
		"held request resumed and returned multiple streamed deltas",
	)
	if err := runToolApproval(ctx, config, third); err != nil {
		return fail("tool-approval", err)
	}
	report.add(
		"tool-approval",
		checkPassed,
		"TodoWrite remained behind the durable allow-once barrier",
	)
	if err := runAgentInterrupt(ctx, config, third); err != nil {
		return fail("agent-sigint", err)
	}
	report.add(
		"agent-sigint",
		checkPassed,
		"captured Claude SIGINT canceled an active streamed Exchange",
	)
	stopContext, stopCancel = context.WithTimeout(ctx, 30*time.Second)
	err = third.stopGracefully(stopContext)
	stopCancel()
	thirdStopped = true
	if err != nil {
		return fail("final-shutdown", err)
	}
	report.add(
		"final-shutdown",
		checkPassed,
		"credentialed assembly generation drained cleanly",
	)
	return report, nil
}

type acceptancePhases struct {
	deterministic config
	credentialed  config
}

func splitAcceptancePhases(input config) (acceptancePhases, error) {
	deterministic, err := isolateDeterministicSecret(input)
	if err != nil {
		return acceptancePhases{}, err
	}
	return acceptancePhases{
		deterministic: deterministic,
		credentialed:  input,
	}, nil
}

func isolateDeterministicSecret(input config) (config, error) {
	suffix, err := idempotencyKey()
	if err != nil {
		return config{}, err
	}
	reference := "secret://provider/m0-assembly-" + suffix
	if _, err := secretstore.ParseReference(reference); err != nil {
		return config{}, fmt.Errorf(
			"construct deterministic missing secret reference: %w",
			err,
		)
	}
	input.secretRef = reference
	return input, nil
}

func verifyFixedClaude(ctx context.Context, config config) error {
	verifier, err := clientadapter.NewM0Verifier(
		[]clientadapter.Release{
			clientadapter.ClaudeCode221220DarwinARM64(),
		},
	)
	if err != nil {
		return err
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return err
	}
	detection, err := verifier.Verify(ctx, clientadapter.Request{
		Command:        []string{config.claudePath, "--version"},
		CWD:            workingDirectory,
		ExecutablePath: config.claudePath,
	})
	if err != nil {
		return err
	}
	if detection.Status != clientadapter.StatusVerified ||
		detection.Evidence == nil ||
		detection.Evidence.ID != "claude-code" ||
		detection.Evidence.Version != "2.1.220" {
		return errors.New("Claude executable did not match the fixed release")
	}
	return nil
}

func requireRecoveredAccess(
	ctx context.Context,
	control *controlClient,
	config config,
) error {
	_, status, problem, err := control.applyAccess(ctx, config, 0)
	if err != nil {
		return err
	}
	if status != http.StatusConflict ||
		problem.ReasonCode != "revision_conflict" {
		return fmt.Errorf(
			"recovered Access CAS status=%d reason=%s",
			status,
			problem.ReasonCode,
		)
	}
	return nil
}

type ingressPreflightEvidence struct {
	opaqueKinds        string
	providerReason     string
	connectionBaseline int64
}

func runHeldIngressPreflight(
	ctx context.Context,
	config config,
	generation *daemonGeneration,
) (ingressPreflightEvidence, error) {
	current, err := generation.control.offline(ctx)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	held, err := generation.control.offlineAction(
		ctx,
		"enter",
		current.Revision,
	)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	if held.State != offlinehold.StateHeld ||
		!held.SafeToDisconnect ||
		held.ActiveEgress != 0 {
		return ingressPreflightEvidence{}, fmt.Errorf(
			"deterministic offline hold did not settle: %+v",
			held,
		)
	}
	activityBaseline, err := latestActivitySequence(
		ctx,
		generation.control,
	)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	connectionBaseline, err := latestConnectionSequence(
		ctx,
		generation.control,
	)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	workingDirectory, err := os.MkdirTemp("", "vibermate-agent-preflight-*")
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	defer os.RemoveAll(workingDirectory)
	run, err := startAgent(
		config,
		workingDirectory,
		"Reply exactly VIBEMATE_PREFLIGHT and nothing else.",
		"",
		"",
	)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	runFinished := false
	defer func() {
		if runFinished {
			return
		}
		_ = run.signalInterrupt()
		waitContext, cancelWait := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		_, _ = run.wait(waitContext)
		cancelWait()
	}()
	opaqueQueued, err := waitForHeldQueuedEgressKind(
		ctx,
		generation.control,
		run,
		offlinehold.EgressOpaque,
	)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	opaqueKinds, err := queuedKindSummary(opaqueQueued)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	releasing, err := generation.control.offlineAction(
		ctx,
		"resume",
		opaqueQueued.Revision,
	)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	if err := requireReleasedOfflineRequest(releasing); err != nil {
		return ingressPreflightEvidence{}, err
	}
	reasonCode, err := waitForExchangeFailureAfter(
		ctx,
		generation.control,
		config.accessID,
		activityBaseline,
		exchange.ReasonProviderCredentialUnavailable,
		45*time.Second,
	)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	signalErr := run.signalInterrupt()
	if signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
		return ingressPreflightEvidence{}, signalErr
	}
	waitContext, cancelWait := context.WithTimeout(ctx, 15*time.Second)
	_, waitErr := run.wait(waitContext)
	cancelWait()
	if waitErr != nil {
		return ingressPreflightEvidence{}, waitErr
	}
	runFinished = true
	_, _, _, markerSeen := run.evidence()
	if markerSeen {
		return ingressPreflightEvidence{}, errors.New(
			"deterministic request completed despite an unconfigured credential",
		)
	}
	if err := waitForOfflineSettlement(
		ctx,
		generation.control,
		10*time.Second,
	); err != nil {
		return ingressPreflightEvidence{}, err
	}
	return ingressPreflightEvidence{
		opaqueKinds:        opaqueKinds,
		providerReason:     reasonCode,
		connectionBaseline: connectionBaseline,
	}, nil
}

type heldAgentRun struct {
	run              *agentRun
	workingDirectory string
	queuedKinds      string
	connectionID     string
}

func queueHeldIngressForDaemonKill(
	ctx context.Context,
	config config,
	generation *daemonGeneration,
) (*heldAgentRun, error) {
	current, err := generation.control.offline(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireSettledOfflineState(current); err != nil {
		return nil, err
	}
	held, err := generation.control.offlineAction(
		ctx,
		"enter",
		current.Revision,
	)
	if err != nil {
		return nil, err
	}
	if held.State != offlinehold.StateHeld ||
		!held.SafeToDisconnect ||
		held.QueuedRequests != 0 ||
		held.HeldBytes != 0 ||
		held.ActiveEgress != 0 {
		return nil, fmt.Errorf(
			"offline hold did not settle before daemon-kill request: %+v",
			held,
		)
	}
	connectionBaseline, err := latestConnectionSequence(
		ctx,
		generation.control,
	)
	if err != nil {
		return nil, err
	}
	workingDirectory, err := os.MkdirTemp(
		"",
		"vibermate-agent-daemon-kill-*",
	)
	if err != nil {
		return nil, err
	}
	run, err := startAgent(
		config,
		workingDirectory,
		"Reply exactly VIBEMATE_KILLED_REQUEST and nothing else.",
		"",
		"VIBEMATE_KILLED_REQUEST",
	)
	if err != nil {
		_ = os.RemoveAll(workingDirectory)
		return nil, err
	}
	cleanup := func(root error) (*heldAgentRun, error) {
		_ = run.signalInterrupt()
		waitContext, cancelWait := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		_, _ = run.wait(waitContext)
		cancelWait()
		_ = os.RemoveAll(workingDirectory)
		return nil, root
	}
	if err := waitForQueuedEgress(ctx, generation.control, run); err != nil {
		return cleanup(err)
	}
	queued, err := generation.control.offline(ctx)
	if err != nil {
		return cleanup(err)
	}
	if queued.State != offlinehold.StateHeld ||
		!queued.SafeToDisconnect ||
		queued.QueuedRequests == 0 ||
		queued.HeldBytes == 0 ||
		queued.ActiveEgress != 0 {
		return cleanup(fmt.Errorf(
			"daemon-kill request did not remain behind the egress gate: %+v",
			queued,
		))
	}
	queuedKinds, err := queuedKindSummary(queued)
	if err != nil {
		return cleanup(err)
	}
	connectionID, err := waitForActiveConnectionAudit(
		ctx,
		generation.control,
		connectionBaseline,
		10*time.Second,
	)
	if err != nil {
		return cleanup(err)
	}
	return &heldAgentRun{
		run:              run,
		workingDirectory: workingDirectory,
		queuedKinds:      queuedKinds,
		connectionID:     connectionID,
	}, nil
}

func queuedKindSummary(snapshot offlinehold.Snapshot) (string, error) {
	kinds := []offlinehold.EgressKind{
		offlinehold.EgressProvider,
		offlinehold.EgressOpaque,
		offlinehold.EgressAuxiliary,
		offlinehold.EgressPlugin,
		offlinehold.EgressUpdate,
		offlinehold.EgressBlindTunnel,
	}
	parts := make([]string, 0, len(kinds))
	total := 0
	for _, kind := range kinds {
		count := snapshot.QueuedByKind[kind]
		if count < 0 {
			return "", errors.New("offline queued-kind count is negative")
		}
		if count == 0 {
			continue
		}
		total += count
		parts = append(parts, fmt.Sprintf("%s:%d", kind, count))
	}
	if total != snapshot.QueuedRequests {
		return "", fmt.Errorf(
			"offline queued-kind total=%d does not match queued requests=%d",
			total,
			snapshot.QueuedRequests,
		)
	}
	if len(parts) == 0 {
		return "", errors.New("offline queued-kind evidence is empty")
	}
	return strings.Join(parts, ","), nil
}

func waitForHeldQueueDrain(
	ctx context.Context,
	control *controlClient,
	limit time.Duration,
) error {
	waitContext, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var latest offlinehold.Snapshot
	for {
		snapshot, err := control.offline(waitContext)
		if err != nil {
			return err
		}
		latest = snapshot
		if snapshot.State == offlinehold.StateHeld &&
			snapshot.SafeToDisconnect &&
			snapshot.QueuedRequests == 0 &&
			snapshot.HeldBytes == 0 &&
			snapshot.ActiveEgress == 0 {
			return nil
		}
		select {
		case <-ticker.C:
		case <-waitContext.Done():
			return fmt.Errorf(
				"held egress queue did not drain after Agent cancellation: %+v: %w",
				latest,
				waitContext.Err(),
			)
		}
	}
}

func runNormalReply(
	ctx context.Context,
	config config,
	generation *daemonGeneration,
) error {
	workingDirectory, err := os.MkdirTemp("", "vibermate-agent-normal-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workingDirectory)
	run, err := startAgent(
		config,
		workingDirectory,
		"Reply exactly VIBEMATE_NORMAL_OK and nothing else.",
		"",
		"VIBEMATE_NORMAL_OK",
	)
	if err != nil {
		return err
	}
	waitContext, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	exitCode, err := run.wait(waitContext)
	if err != nil || exitCode != 0 {
		processFailure := agentProcessFailure("normal", exitCode, err, run)
		reasonCode, activityErr := latestExchangeFailure(
			ctx,
			generation.control,
			config.accessID,
		)
		if activityErr == nil && reasonCode != "" {
			return fmt.Errorf(
				"%w exchangeActivity=%s",
				processFailure,
				reasonCode,
			)
		}
		offlineSnapshot, offlineErr := generation.control.offline(ctx)
		if offlineErr == nil {
			return fmt.Errorf(
				"%w offlineState=%s queued=%d active=%d",
				processFailure,
				offlineSnapshot.State,
				offlineSnapshot.QueuedRequests,
				offlineSnapshot.ActiveEgress,
			)
		}
		return processFailure
	}
	_, deltas, _, marker := run.evidence()
	if deltas == 0 || !marker {
		return fmt.Errorf(
			"normal reply evidence deltas=%d marker=%t",
			deltas,
			marker,
		)
	}
	return waitForOfflineSettlement(ctx, generation.control, 10*time.Second)
}

func latestExchangeFailure(
	ctx context.Context,
	control *controlClient,
	accessID string,
) (string, error) {
	page, err := control.activities(ctx)
	if err != nil {
		return "", err
	}
	for _, record := range page.Items {
		if record.Kind == activity.KindExchangeCompleted &&
			record.AccessID == accessID &&
			record.Status == activity.StatusFailed {
			return record.ReasonCode, nil
		}
	}
	return "", nil
}

func latestActivitySequence(
	ctx context.Context,
	control *controlClient,
) (int64, error) {
	page, err := control.activities(ctx)
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, record := range page.Items {
		if record.Sequence > latest {
			latest = record.Sequence
		}
	}
	return latest, nil
}

func latestConnectionSequence(
	ctx context.Context,
	control *controlClient,
) (int64, error) {
	page, err := control.connections(ctx, "")
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, record := range page.Items {
		if record.Sequence > latest {
			latest = record.Sequence
		}
	}
	return latest, nil
}

func connectionRecordsAfter(
	ctx context.Context,
	control *controlClient,
	after int64,
) ([]connectionevent.Record, error) {
	if after < 0 {
		return nil, errors.New("ConnectionEvent baseline is invalid")
	}
	const maxPages = 32
	records := make([]connectionevent.Record, 0)
	cursor := ""
	seenCursors := make(map[string]struct{})
	for range maxPages {
		page, err := control.connections(ctx, cursor)
		if err != nil {
			return nil, err
		}
		reachedBaseline := false
		for _, record := range page.Items {
			if record.Sequence <= after {
				reachedBaseline = true
				continue
			}
			records = append(records, record)
		}
		if reachedBaseline || page.NextCursor == "" {
			return records, nil
		}
		if page.NextCursor == cursor {
			return nil, errors.New("ConnectionEvent cursor did not advance")
		}
		if _, exists := seenCursors[page.NextCursor]; exists {
			return nil, errors.New("ConnectionEvent cursor repeated")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	return nil, errors.New("ConnectionEvent evidence exceeded its page bound")
}

func waitForProviderConnectionAudit(
	ctx context.Context,
	control *controlClient,
	config config,
	after int64,
	limit time.Duration,
) (string, error) {
	providerOrigin, err := access.NewProviderOrigin(config.providerOrigin)
	if err != nil {
		return "", err
	}
	clientOrigin, err := access.NewClientOrigin("https://api.anthropic.com")
	if err != nil {
		return "", err
	}
	expectedCredential := assemblyIdentifiers(config.accessID).account
	waitContext, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		records, err := connectionRecordsAfter(waitContext, control, after)
		if err != nil {
			return "", err
		}
		seen := make(map[string]struct{})
		for _, record := range records {
			if record.RequestedHost != clientOrigin.EndpointAuthority() {
				continue
			}
			if _, exists := seen[record.ConnectionID]; exists {
				continue
			}
			seen[record.ConnectionID] = struct{}{}
			timeline, err := control.connectionTimeline(
				waitContext,
				record.ConnectionID,
			)
			if err != nil {
				return "", err
			}
			ready, err := providerConnectionAuditReady(
				timeline,
				clientOrigin,
				providerOrigin,
				expectedCredential,
			)
			if err != nil {
				lastErr = err
				continue
			}
			if ready {
				return record.ConnectionID, nil
			}
		}
		select {
		case <-ticker.C:
		case <-waitContext.Done():
			if lastErr != nil {
				return "", lastErr
			}
			return "", fmt.Errorf(
				"provider ConnectionEvent evidence did not become terminal: %w",
				waitContext.Err(),
			)
		}
	}
}

func providerConnectionAuditReady(
	timeline connectionevent.Timeline,
	clientOrigin access.ClientOrigin,
	providerOrigin access.ProviderOrigin,
	expectedCredential string,
) (bool, error) {
	if len(timeline.Events) == 0 {
		return false, errors.New("provider ConnectionEvent timeline is empty")
	}
	first := timeline.Events[0]
	if first.ConnectionID != timeline.ConnectionID ||
		first.Phase != connectionevent.PhaseAttempted ||
		first.SourceConfidence != connectionevent.SourceConfidenceUnknown ||
		first.RequestedHost != clientOrigin.EndpointAuthority() ||
		first.Port != clientOrigin.Port() {
		return false, fmt.Errorf(
			"provider ConnectionEvent attempt is invalid: %+v",
			first,
		)
	}
	allowSeen := false
	clientConnected := false
	providerConnected := false
	previousSequence := int64(0)
	for _, record := range timeline.Events {
		if err := record.Validate(); err != nil {
			return false, err
		}
		if record.ConnectionID != timeline.ConnectionID ||
			record.RequestedHost != clientOrigin.EndpointAuthority() ||
			record.Sequence <= previousSequence {
			return false, errors.New(
				"provider ConnectionEvent identity or ordering changed",
			)
		}
		previousSequence = record.Sequence
		if record.Decision == connectionevent.DecisionAllow {
			if record.SourceConfidence !=
				connectionevent.SourceConfidenceConfigured ||
				record.IngressID == "" ||
				record.SourceLabel == "" ||
				record.RuleID != "m0.agent_endpoint_exact" ||
				record.EgressScope != connectionevent.EgressScopeAccess ||
				record.EgressSource !=
					connectionevent.EgressSourceAccessDefault ||
				record.EgressPolicyRevision != 1 ||
				record.Decryption != connectionevent.DecryptionMITM {
				return false, fmt.Errorf(
					"provider ConnectionEvent allow evidence is invalid: %+v",
					record,
				)
			}
			allowSeen = true
		}
		if record.Phase != connectionevent.PhaseConnected {
			continue
		}
		if record.ObservedSNI == clientOrigin.TLSServerName() &&
			record.RouteHost == clientOrigin.TLSServerName() {
			clientConnected = true
		}
		if record.RouteHost == providerOrigin.TLSServerName() {
			if record.CredentialBindingID != expectedCredential {
				return false, fmt.Errorf(
					"provider ConnectionEvent credential binding=%q",
					record.CredentialBindingID,
				)
			}
			providerConnected = true
		}
	}
	if !providerConnected {
		return false, nil
	}
	if !allowSeen || !clientConnected {
		return false, errors.New(
			"provider ConnectionEvent omitted ingress decision evidence",
		)
	}
	last := timeline.Events[len(timeline.Events)-1]
	if !connectionRecordTerminal(last) {
		return false, nil
	}
	if last.Phase != connectionevent.PhaseClosed &&
		last.Phase != connectionevent.PhaseFailed {
		return false, fmt.Errorf(
			"provider ConnectionEvent terminal phase=%q",
			last.Phase,
		)
	}
	return true, nil
}

func waitForActiveConnectionAudit(
	ctx context.Context,
	control *controlClient,
	after int64,
	limit time.Duration,
) (string, error) {
	clientOrigin, err := access.NewClientOrigin("https://api.anthropic.com")
	if err != nil {
		return "", err
	}
	waitContext, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, err := connectionRecordsAfter(waitContext, control, after)
		if err != nil {
			return "", err
		}
		seen := make(map[string]struct{})
		for _, record := range records {
			if record.RequestedHost != clientOrigin.EndpointAuthority() {
				continue
			}
			if _, exists := seen[record.ConnectionID]; exists {
				continue
			}
			seen[record.ConnectionID] = struct{}{}
			timeline, err := control.connectionTimeline(
				waitContext,
				record.ConnectionID,
			)
			if err != nil {
				return "", err
			}
			if len(timeline.Events) == 0 {
				continue
			}
			last := timeline.Events[len(timeline.Events)-1]
			if last.Sequence <= after ||
				connectionRecordTerminal(last) ||
				last.Phase != connectionevent.PhaseConnected ||
				last.Decision != connectionevent.DecisionAllow ||
				last.SourceConfidence !=
					connectionevent.SourceConfidenceConfigured ||
				last.IngressID == "" ||
				last.RequestedHost != clientOrigin.EndpointAuthority() ||
				last.EgressPolicyRevision != 1 {
				continue
			}
			return timeline.ConnectionID, nil
		}
		select {
		case <-ticker.C:
		case <-waitContext.Done():
			return "", fmt.Errorf(
				"active held ConnectionEvent was not observed: %w",
				waitContext.Err(),
			)
		}
	}
}

func requireRecoveredConnectionAudit(
	ctx context.Context,
	control *controlClient,
	connectionID string,
) error {
	if connectionID == "" {
		return errors.New("interrupted ConnectionEvent ID is empty")
	}
	timeline, err := control.connectionTimeline(ctx, connectionID)
	if err != nil {
		return err
	}
	if len(timeline.Events) < 2 {
		return errors.New("recovered ConnectionEvent timeline is incomplete")
	}
	recoveryCount := 0
	for _, record := range timeline.Events {
		if record.ErrorClass == connectionevent.RecoveryErrorClass {
			recoveryCount++
		}
	}
	if recoveryCount != 1 {
		return fmt.Errorf(
			"ConnectionEvent recovery terminal count=%d",
			recoveryCount,
		)
	}
	previous := timeline.Events[len(timeline.Events)-2]
	last := timeline.Events[len(timeline.Events)-1]
	if err := last.Validate(); err != nil {
		return err
	}
	if connectionRecordTerminal(previous) {
		return errors.New(
			"ConnectionEvent was terminal before daemon recovery",
		)
	}
	if last.Sequence <= previous.Sequence ||
		last.Phase != connectionevent.PhaseFailed ||
		last.Outcome != connectionevent.OutcomeFailed ||
		last.ErrorClass != connectionevent.RecoveryErrorClass ||
		last.EndedAt.IsZero() ||
		last.EndedAt.Before(last.StartedAt) {
		return fmt.Errorf(
			"recovered ConnectionEvent terminal is invalid: %+v",
			last,
		)
	}
	return nil
}

func connectionRecordTerminal(record connectionevent.Record) bool {
	return record.Phase == connectionevent.PhaseClosed ||
		record.Phase == connectionevent.PhaseFailed ||
		(record.Phase == connectionevent.PhaseDecided &&
			record.Decision == connectionevent.DecisionDeny)
}

func waitForExchangeFailureAfter(
	ctx context.Context,
	control *controlClient,
	accessID string,
	after int64,
	expected exchange.ReasonCode,
	limit time.Duration,
) (string, error) {
	waitContext, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		page, err := control.activities(waitContext)
		if err != nil {
			return "", err
		}
		var latest *activity.Record
		for index := range page.Items {
			record := &page.Items[index]
			if record.Sequence <= after ||
				record.Kind != activity.KindExchangeCompleted ||
				record.AccessID != accessID ||
				record.Status != activity.StatusFailed {
				continue
			}
			if latest == nil || record.Sequence > latest.Sequence {
				latest = record
			}
		}
		if latest != nil {
			if latest.ReasonCode != string(expected) {
				return "", fmt.Errorf(
					"deterministic provider failure reason=%q, want %q",
					latest.ReasonCode,
					expected,
				)
			}
			return latest.ReasonCode, nil
		}
		select {
		case <-ticker.C:
		case <-waitContext.Done():
			return "", fmt.Errorf(
				"expected Exchange failure was not recorded: %w",
				waitContext.Err(),
			)
		}
	}
}

func runHeldStreaming(
	ctx context.Context,
	config config,
	generation *daemonGeneration,
) error {
	hold, err := generation.control.offline(ctx)
	if err != nil {
		return err
	}
	hold, err = generation.control.offlineAction(ctx, "enter", hold.Revision)
	if err != nil {
		return err
	}
	if hold.State != offlinehold.StateHeld || !hold.SafeToDisconnect {
		return fmt.Errorf("offline hold did not settle: %+v", hold)
	}
	workingDirectory, err := os.MkdirTemp("", "vibermate-agent-stream-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workingDirectory)
	run, err := startAgent(
		config,
		workingDirectory,
		"Write two short sentences, then end exactly with VIBEMATE_STREAM_OK.",
		"",
		"VIBEMATE_STREAM_OK",
	)
	if err != nil {
		return err
	}
	if err := waitForQueuedEgress(ctx, generation.control, run); err != nil {
		return err
	}
	current, err := generation.control.offline(ctx)
	if err != nil {
		return err
	}
	resumed, err := generation.control.offlineAction(
		ctx,
		"resume",
		current.Revision,
	)
	if err != nil {
		return err
	}
	if err := requireReleasedOfflineRequest(resumed); err != nil {
		return err
	}
	waitContext, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	exitCode, err := run.wait(waitContext)
	if err != nil || exitCode != 0 {
		return agentProcessFailure("streaming", exitCode, err, run)
	}
	_, deltas, _, marker := run.evidence()
	if deltas < 2 || !marker {
		return fmt.Errorf(
			"streaming evidence deltas=%d marker=%t",
			deltas,
			marker,
		)
	}
	return waitForOfflineSettlement(ctx, generation.control, 10*time.Second)
}

func requireReleasedOfflineRequest(snapshot offlinehold.Snapshot) error {
	if snapshot.State != offlinehold.StateReleasing ||
		snapshot.QueuedRequests != 0 ||
		snapshot.ActiveEgress == 0 {
		return fmt.Errorf(
			"offline resume did not release the queued request: %+v",
			snapshot,
		)
	}
	return nil
}

func requireSettledOfflineState(snapshot offlinehold.Snapshot) error {
	if snapshot.State != offlinehold.StateOnline ||
		snapshot.QueuedRequests != 0 ||
		snapshot.ActiveEgress != 0 {
		return fmt.Errorf(
			"offline hold did not settle after streamed completion: %+v",
			snapshot,
		)
	}
	return nil
}

func waitForOfflineSettlement(
	ctx context.Context,
	control *controlClient,
	limit time.Duration,
) error {
	waitContext, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var latest offlinehold.Snapshot
	for {
		snapshot, err := control.offline(waitContext)
		if err != nil {
			return err
		}
		latest = snapshot
		if requireSettledOfflineState(snapshot) == nil {
			return nil
		}
		select {
		case <-ticker.C:
		case <-waitContext.Done():
			return fmt.Errorf(
				"offline hold did not settle before deadline: %+v: %w",
				latest,
				waitContext.Err(),
			)
		}
	}
}

func waitForQueuedEgress(
	ctx context.Context,
	control *controlClient,
	run *agentRun,
) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err := control.offline(ctx)
		if err != nil {
			return err
		}
		if snapshot.QueuedRequests > 0 {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		case <-run.done:
			return errors.New("Claude exited before egress queued in offline hold")
		}
	}
}

func waitForHeldQueuedEgressKind(
	ctx context.Context,
	control *controlClient,
	run *agentRun,
	kind offlinehold.EgressKind,
) (offlinehold.Snapshot, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err := control.offline(ctx)
		if err != nil {
			return offlinehold.Snapshot{}, err
		}
		if snapshot.State == offlinehold.StateHeld &&
			snapshot.SafeToDisconnect &&
			snapshot.ActiveEgress == 0 &&
			snapshot.QueuedByKind[kind] > 0 {
			return snapshot, nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return offlinehold.Snapshot{}, ctx.Err()
		case <-run.done:
			return offlinehold.Snapshot{}, fmt.Errorf(
				"Claude exited before %s egress queued in offline hold",
				kind,
			)
		}
	}
}

func runToolApproval(
	ctx context.Context,
	config config,
	generation *daemonGeneration,
) error {
	workingDirectory, err := os.MkdirTemp("", "vibermate-agent-tool-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workingDirectory)
	run, err := startAgent(
		config,
		workingDirectory,
		"Use TodoWrite exactly once to record one completed item, then reply VIBEMATE_TOOL_DONE.",
		"TodoWrite",
		"VIBEMATE_TOOL_DONE",
	)
	if err != nil {
		return err
	}
	approval, err := waitForApproval(ctx, generation.control, run)
	if err != nil {
		return err
	}
	if len(approval.ToolNames) != 1 ||
		approval.ToolNames[0] != "TodoWrite" {
		return fmt.Errorf("unexpected approval tools: %v", approval.ToolNames)
	}
	_, _, toolUsesBeforeDecision, markerBeforeDecision := run.evidence()
	if toolUsesBeforeDecision != 0 || markerBeforeDecision {
		return fmt.Errorf(
			"tool stream leaked before approval: toolUses=%d marker=%t",
			toolUsesBeforeDecision,
			markerBeforeDecision,
		)
	}
	resolved, err := generation.control.allowOnce(ctx, approval)
	if err != nil {
		return err
	}
	if resolved.State != toolapproval.StateAllowed {
		return fmt.Errorf("approval did not resolve allowed: %+v", resolved)
	}
	waitContext, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	exitCode, err := run.wait(waitContext)
	if err != nil || exitCode != 0 {
		return agentProcessFailure("tool", exitCode, err, run)
	}
	_, _, toolUses, marker := run.evidence()
	if toolUses == 0 || !marker {
		return fmt.Errorf(
			"tool approval evidence toolUses=%d marker=%t",
			toolUses,
			marker,
		)
	}
	return nil
}

func agentProcessFailure(
	label string,
	exitCode int,
	waitErr error,
	run *agentRun,
) error {
	message := fmt.Sprintf("%s Claude exit=%d", label, exitCode)
	if evidence := run.safeFailureEvidence(); evidence != "" {
		message += " " + evidence
	}
	if waitErr != nil {
		return fmt.Errorf("%s: %w", message, waitErr)
	}
	return errors.New(message)
}

func waitForApproval(
	ctx context.Context,
	control *controlClient,
	run *agentRun,
) (toolapproval.View, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		page, err := control.pendingApprovals(ctx)
		if err != nil {
			return toolapproval.View{}, err
		}
		if len(page.Items) != 0 {
			slices.SortFunc(page.Items, func(left, right toolapproval.View) int {
				return left.CreatedAt.Compare(right.CreatedAt)
			})
			return page.Items[0], nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return toolapproval.View{}, ctx.Err()
		case <-run.done:
			return toolapproval.View{}, agentExitedBeforeApproval(ctx, run)
		}
	}
}

func agentExitedBeforeApproval(
	ctx context.Context,
	run *agentRun,
) error {
	exitCode, waitErr := run.wait(ctx)
	return fmt.Errorf(
		"Claude exited before a tool approval became pending: %w",
		agentProcessFailure("tool pre-approval", exitCode, waitErr, run),
	)
}

func runAgentInterrupt(
	ctx context.Context,
	config config,
	generation *daemonGeneration,
) error {
	workingDirectory, err := os.MkdirTemp("", "vibermate-agent-sigint-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workingDirectory)
	run, err := startAgent(
		config,
		workingDirectory,
		"Write a detailed two-thousand-word explanation of deterministic concurrency testing.",
		"",
		"",
	)
	if err != nil {
		return err
	}
	deltaContext, cancelDelta := context.WithTimeout(ctx, 2*time.Minute)
	err = run.waitForDelta(deltaContext)
	cancelDelta()
	if err != nil {
		return err
	}
	if err := run.signalInterrupt(); err != nil {
		return err
	}
	waitContext, cancelWait := context.WithTimeout(ctx, 20*time.Second)
	defer cancelWait()
	_, err = run.wait(waitContext)
	if err != nil {
		return err
	}
	status, err := generation.control.status(ctx)
	if err != nil {
		return err
	}
	if !status.Ready ||
		status.Runtime.State != "initialized" {
		return errors.New("runtime did not remain ready after Agent SIGINT")
	}
	return waitForOfflineSettlement(ctx, generation.control, 10*time.Second)
}
