package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/acceptancereport"
	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

const packagedAcceptanceLockName = "packaged-acceptance.lock"

func runAcceptance(
	parent context.Context,
	config config,
) (report acceptanceReport, resultErr error) {
	client, clientErr := selectedAcceptanceClient(config)
	report = newReport(time.Now(), client)
	defer func() {
		report.FinishedAt = time.Now().UTC()
	}()
	if clientErr != nil {
		return report, clientErr
	}
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

	adapterEvidence, err := verifyFixedClient(ctx, config)
	if err != nil {
		return fail("fixed-client-identity", err)
	}
	if err := report.bindClientEvidence(adapterEvidence); err != nil {
		return fail("fixed-client-identity", err)
	}
	report.add(
		"fixed-client-identity",
		checkPassed,
		client.ReportLabel+" "+client.Version+
			" compound release evidence matched",
	)
	layout, err := runtimepath.Default()
	if err != nil {
		return fail("runtime-layout", err)
	}
	acceptanceGuard, err := instanceguard.Acquire(filepath.Join(
		layout.AppCacheDirectory,
		packagedAcceptanceLockName,
	))
	if err != nil {
		return fail("exclusive-generation-preflight", err)
	}
	defer acceptanceGuard.Release()
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
		"no other Desktop generation owned the runtime and this packaged acceptance owns the per-user serialization lock",
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
		return fail("packaged-main-navigation-cold-restore", err)
	}
	report.add(
		"packaged-desktop-shell",
		checkPassed,
		"packaged App used an isolated user directory, exchanged bootstrap, and gracefully drained its sidecar",
	)
	report.add(
		"packaged-main-navigation-cold-restore",
		checkPassed,
		"two fresh packaged main-window launches restored one non-default private locator, atomically persisted Router mount state, and flushed it on exit",
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
		applied.ApplicationState != desktopcontrol.AccessApplicationStateActive ||
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
	report.add(
		"access-apply",
		checkPassed,
		"revision 1 executable Access committed and published",
	)
	if err := configureAcceptanceConnectionPolicy(
		ctx,
		first.control,
		config,
	); err != nil {
		return fail("fixed-client-connection-policy", err)
	}
	report.add(
		"fixed-client-connection-policy",
		checkPassed,
		"an explicit exact-host-and-port rule admitted only the fixed client origin; the default remained ask",
	)
	firstAudit, err := openExchangeAuditReader(ctx, dataDirectory)
	if err != nil {
		return fail("fixed-client-ingress-preflight", err)
	}
	defer firstAudit.Close()
	preflight, err := runHeldIngressPreflight(ctx, config, first, firstAudit)
	if err != nil {
		return fail("fixed-client-ingress-preflight", err)
	}
	report.add(
		"fixed-client-ingress-preflight",
		checkPassed,
		"fixed client crossed its exact ingress and controlled-egress boundary; queuedKinds="+preflight.queuedKinds,
	)
	if client.ID == acceptanceClientCodexCLI {
		fallbackDetail, err := preflight.codexHTTPFallback.reportDetail()
		if err != nil {
			return fail(
				"fixed-codex-http-fallback",
				err,
			)
		}
		report.add(
			"fixed-codex-http-fallback",
			checkPassed,
			fallbackDetail,
		)
	}
	report.add(
		"fixed-client-provider-fail-closed",
		checkPassed,
		"the subsequent provider request failed before transport at the missing credential boundary; reason="+preflight.providerReason,
	)
	connectionID, err := waitForClientConnectionAudit(
		ctx,
		first.control,
		config,
		preflight.connectionBaseline,
		15*time.Second,
	)
	if err != nil {
		return fail("fixed-client-connection-audit", err)
	}
	report.add(
		"fixed-client-connection-audit",
		checkPassed,
		"durable MITM ConnectionEvent bound the verified fixed client to the explicit exact-host-and-port rule without request-level provider facts; connectionId="+connectionID,
	)
	stopContext, stopCancel := context.WithTimeout(ctx, 30*time.Second)
	err = first.stopGracefully(stopContext)
	stopCancel()
	firstStopped = true
	if err != nil {
		return fail("daemon-sigint", err)
	}
	if err := firstAudit.Close(); err != nil {
		return fail("sqlite-reopen", err)
	}
	if err := requireReopenedExchangeAudit(
		ctx,
		dataDirectory,
		nil,
		preflight.providerFailure,
	); err != nil {
		return fail("sqlite-reopen", err)
	}
	removed, err := discoveryRemoved(layout.CLIControlRecord)
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
	if err := requireReopenedExchangeAudit(
		ctx,
		dataDirectory,
		second.control,
		preflight.providerFailure,
	); err != nil {
		return fail("sqlite-reopen", err)
	}
	report.add(
		"sqlite-reopen",
		checkPassed,
		"new incarnation recovered Access revision 1 and the exact committed Exchange failure from SQLite",
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
		"SIGKILL terminated a held fixed-client request without an upstream send or completion marker; queuedKinds="+heldAgent.queuedKinds,
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
	if err := requireReopenedExchangeAudit(
		ctx,
		dataDirectory,
		third.control,
		preflight.providerFailure,
	); err != nil {
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
		if err := requireDeterministicCheckContract(report); err != nil {
			return fail("acceptance-report-contract", err)
		}
		return report, nil
	}
	stopContext, stopCancel = context.WithTimeout(ctx, 30*time.Second)
	err = third.stopGracefully(stopContext)
	stopCancel()
	thirdStopped = true
	if err != nil {
		return fail("deterministic-phase-shutdown", err)
	}
	report.add(
		"deterministic-phase-shutdown",
		checkPassed,
		"the isolated deterministic generation drained before credentialed state was created",
	)

	credentialedDataDirectory, err := os.MkdirTemp(
		"",
		"vibermate-credentialed-assembly-*",
	)
	if err != nil {
		return fail("credentialed-private-data-directory", err)
	}
	if err := privateDirectory(credentialedDataDirectory); err != nil {
		return fail("credentialed-private-data-directory", err)
	}
	if !config.keepData {
		defer cleanTemporaryData(credentialedDataDirectory)
	}
	report.add(
		"credentialed-private-data-directory",
		checkPassed,
		"credentialed acceptance uses a second private runtime data directory",
	)

	config = phases.credentialed
	credentialed, err := startDaemon(
		ctx,
		config,
		layout.AppCacheDirectory,
		credentialedDataDirectory,
	)
	if err != nil {
		return fail("credentialed-daemon-start", err)
	}
	credentialedStopped := false
	defer func() {
		if !credentialedStopped {
			stopContext, stopCancel := context.WithTimeout(
				context.Background(),
				10*time.Second,
			)
			_ = credentialed.stopGracefully(stopContext)
			stopCancel()
		}
	}()
	credentialedApplied, credentialedStatus, credentialedProblem, credentialedErr :=
		credentialed.control.applyAccess(ctx, config, 0)
	if credentialedErr != nil ||
		credentialedStatus != http.StatusOK ||
		credentialedProblem.ReasonCode != "" ||
		credentialedApplied.Outcome != access.WriteOutcomeCommitted ||
		credentialedApplied.Revision != 1 ||
		credentialedApplied.ApplicationState != desktopcontrol.AccessApplicationStateActive ||
		len(credentialedApplied.PlanHash) != 64 {
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
	if err := configureAcceptanceConnectionPolicy(
		ctx,
		credentialed.control,
		config,
	); err != nil {
		return fail("credentialed-connection-policy", err)
	}
	report.add(
		"credentialed-access-apply",
		checkPassed,
		"revision 1 created an isolated credentialed Access without exposing the logical reference or secret value",
	)
	credential, err := credentialed.control.credential(ctx, config)
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
	credentialedAudit, err := openExchangeAuditReader(
		ctx,
		credentialedDataDirectory,
	)
	if err != nil {
		return fail("normal-streaming-reply", err)
	}
	defer credentialedAudit.Close()

	if err := runNormalReply(
		ctx,
		config,
		credentialed,
		credentialedAudit,
	); err != nil {
		return fail("normal-streaming-reply", err)
	}
	report.add(
		"normal-streaming-reply",
		checkPassed,
		"fixed "+client.ReportLabel+
			" completed an unheld streamed provider reply",
	)
	if client.ID == acceptanceClientCodexCLI {
		if err := runCodexResume(
			ctx,
			config,
			credentialed,
			credentialedAudit,
		); err != nil {
			return fail("fixed-codex-exec-resume", err)
		}
		report.add(
			"fixed-codex-exec-resume",
			checkPassed,
			"two successful Exchanges retained one private Codex thread identity across exec resume",
		)
	}
	toolEvidence, err := runToolApproval(ctx, config, credentialed)
	if err != nil {
		return fail("tool-approval", err)
	}
	toolDetail, err := toolEvidence.reportDetail()
	if err != nil {
		return fail("tool-approval", err)
	}
	report.add(
		"tool-approval",
		checkPassed,
		toolDetail,
	)
	streamingEvidence, err := runHeldStreaming(ctx, config, credentialed)
	if err != nil {
		return fail("planned-hold-streaming", err)
	}
	streamingDetail, err := streamingEvidence.reportDetail()
	if err != nil {
		return fail("planned-hold-streaming", err)
	}
	report.add(
		"planned-hold-streaming",
		checkPassed,
		streamingDetail,
	)
	if err := runAgentInterrupt(ctx, config, credentialed); err != nil {
		return fail("agent-sigint", err)
	}
	report.add(
		"agent-sigint",
		checkPassed,
		"captured "+client.ReportLabel+
			" SIGINT canceled an active streamed Exchange",
	)
	if client.ID == acceptanceClientCodexCLI {
		report.add(
			"fixed-codex-http-scope",
			checkPassed,
			"evidence covers bounded WebSocket rejection and Responses HTTP fallback; it does not prove successful Responses WebSocket semantics or TUI interaction",
		)
	}
	stopContext, stopCancel = context.WithTimeout(ctx, 30*time.Second)
	err = credentialed.stopGracefully(stopContext)
	stopCancel()
	credentialedStopped = true
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

func requireDeterministicCheckContract(report acceptanceReport) error {
	if report.Schema != acceptancereport.SchemaV6 {
		return errors.New("deterministic report producer schema is not current")
	}
	required, err := acceptancereport.RequiredCheckIDs(
		report.Client.ID,
		report.Client.Version,
	)
	if err != nil {
		return err
	}
	if len(report.Checks) != len(required) {
		return errors.New("deterministic report check set is incomplete or contains extras")
	}
	for index, id := range required {
		if report.Checks[index].ID != id ||
			report.Checks[index].Status != checkPassed {
			return fmt.Errorf(
				"deterministic report check contract differs at position %d",
				index,
			)
		}
	}
	return nil
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

const acceptanceConnectionRuleID = "acceptance.allow-client-origin"

func configureAcceptanceConnectionPolicy(
	ctx context.Context,
	control *controlClient,
	config config,
) error {
	if ctx == nil || control == nil {
		return errors.New("acceptance connection policy dependencies are required")
	}
	current, err := control.connectionRules(ctx)
	if err != nil {
		return err
	}
	input, err := acceptanceConnectionRuleSet(config, current)
	if err != nil {
		return err
	}
	replaced, err := control.replaceConnectionRules(
		ctx,
		current.Revision,
		input,
	)
	if err != nil {
		return err
	}
	if replaced.Revision != current.Revision+1 ||
		!slices.Equal(replaced.Rules, input.Rules) ||
		replaced.Default != input.Default {
		return fmt.Errorf(
			"connection rule replacement was not exact: %+v",
			replaced,
		)
	}
	return nil
}

func acceptanceConnectionRuleSet(
	config config,
	current desktopcontrol.ConnectionRuleSetResponse,
) (desktopcontrol.ConnectionRuleSetInput, error) {
	if current.Revision == 0 ||
		len(current.Rules) != 0 ||
		current.Default.Decision != "ask" ||
		current.Default.Match != "any" ||
		current.Default.Host != "" ||
		current.Default.Port != 0 {
		return desktopcontrol.ConnectionRuleSetInput{}, fmt.Errorf(
			"acceptance requires a fresh fail-closed connection rule set: %+v",
			current,
		)
	}
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return desktopcontrol.ConnectionRuleSetInput{}, err
	}
	origin, err := access.NewClientOrigin(client.ClientOrigin)
	if err != nil {
		return desktopcontrol.ConnectionRuleSetInput{}, err
	}
	return desktopcontrol.ConnectionRuleSetInput{
		Rules: []desktopcontrol.ConnectionRuleInput{{
			ID:       acceptanceConnectionRuleID,
			Priority: 100,
			Decision: "allow",
			Match:    "exact_host_port",
			Host:     origin.TLSServerName(),
			Port:     origin.Port(),
		}},
		Default: current.Default,
	}, nil
}

func verifyFixedClient(
	ctx context.Context,
	config config,
) (clientadapter.Evidence, error) {
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return clientadapter.Evidence{}, err
	}
	verifier, err := clientadapter.NewReleaseVerifier(
		clientadapter.BuiltInCatalog(),
	)
	if err != nil {
		return clientadapter.Evidence{}, err
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return clientadapter.Evidence{}, err
	}
	detection, err := verifier.Verify(ctx, clientadapter.Request{
		Command:        []string{client.ExecutablePath},
		CWD:            workingDirectory,
		ExecutablePath: client.ExecutablePath,
	})
	if err != nil {
		return clientadapter.Evidence{}, err
	}
	if detection.Status != clientadapter.StatusVerified ||
		detection.Evidence == nil ||
		detection.Evidence.ID != client.Release.ID ||
		detection.Evidence.Revision != client.Release.Revision ||
		detection.Evidence.Version != client.Release.Version ||
		detection.Evidence.LaunchRecipe != client.Release.LaunchRecipe ||
		detection.Evidence.Features != client.Release.Features {
		return clientadapter.Evidence{}, errors.New(
			"client executable did not match the selected fixed release",
		)
	}
	if err := detection.Evidence.Validate(); err != nil {
		return clientadapter.Evidence{}, err
	}
	return *detection.Evidence, nil
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

func requireReopenedExchangeAudit(
	ctx context.Context,
	dataDirectory string,
	control *controlClient,
	expected exchangeAuditRecord,
) (resultErr error) {
	audit, err := openExchangeAuditReader(ctx, dataDirectory)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, audit.Close())
	}()
	if err := requireExchangeAuditRecord(ctx, audit, expected); err != nil {
		return err
	}
	if control != nil {
		return requireCanonicalExchangeSummary(ctx, control, expected)
	}
	return nil
}

func requireCanonicalExchangeSummary(
	ctx context.Context,
	control *controlClient,
	expected exchangeAuditRecord,
) error {
	if ctx == nil || control == nil {
		return errors.New("canonical Exchange summary dependencies are required")
	}
	if err := expected.validate(); err != nil {
		return err
	}
	const maxPages = 32
	cursor := ""
	seenCursors := make(map[string]struct{})
	found := false
	for range maxPages {
		page, err := control.activities(ctx, cursor)
		if err != nil {
			return err
		}
		for _, summary := range page.Items {
			if summary.ID != expected.ExchangeID {
				continue
			}
			if summary.AccessID != expected.AccessID ||
				summary.Status != string(expected.Status) {
				return fmt.Errorf(
					"canonical Exchange summary does not match its committed audit: %+v",
					summary,
				)
			}
			if found {
				return errors.New("canonical Exchange summary is duplicated")
			}
			found = true
		}
		if page.NextCursor == "" {
			if !found {
				return errors.New("canonical Exchange summary is missing")
			}
			return nil
		}
		if page.NextCursor == cursor {
			return errors.New("canonical Activity cursor did not advance")
		}
		if _, repeated := seenCursors[page.NextCursor]; repeated {
			return errors.New("canonical Activity cursor repeated")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	return errors.New("canonical Exchange summary exceeded its page bound")
}

type ingressPreflightEvidence struct {
	queuedKinds        string
	providerReason     string
	providerFailure    exchangeAuditRecord
	connectionBaseline int64
	codexHTTPFallback  codexHTTPFallbackEvidence
}

type codexHTTPFallbackEvidence struct {
	ClientHTTPStatus int
	RuntimeReason    exchange.ReasonCode
	ConnectionAudit  bool
}

const (
	claudeStartupControlRequestCount = 2
	claudeStartupQueueStableFor      = 200 * time.Millisecond
)

func (evidence codexHTTPFallbackEvidence) validate() error {
	if evidence.ClientHTTPStatus != http.StatusUpgradeRequired ||
		evidence.RuntimeReason !=
			exchange.ReasonProviderCredentialUnavailable ||
		!evidence.ConnectionAudit {
		return errors.New(
			"Codex HTTP fallback requires the typed HTTP status, runtime reason, and proxy connection audit",
		)
	}
	return nil
}

func (evidence codexHTTPFallbackEvidence) reportDetail() (string, error) {
	if err := evidence.validate(); err != nil {
		return "", err
	}
	return "fixed Codex surfaced HTTP 426, the proxy audit proved the bounded transition to HTTP, and Runtime Activity bound that request to provider_credential_unavailable", nil
}

func completeCodexHTTPFallbackEvidence(
	ctx context.Context,
	run *agentRun,
	evidence codexHTTPFallbackEvidence,
) (codexHTTPFallbackEvidence, error) {
	if ctx == nil || run == nil {
		return codexHTTPFallbackEvidence{}, errors.New(
			"complete Codex HTTP fallback evidence dependencies are required",
		)
	}
	if !evidence.ConnectionAudit {
		return evidence, errors.New(
			"Codex HTTP fallback connection audit is required before completion",
		)
	}
	if err := run.waitForAgentStatus(
		ctx,
		http.StatusUpgradeRequired,
	); err != nil {
		return evidence, fmt.Errorf(
			"observe Codex fallback HTTP status: %w (%s)",
			err,
			run.safeFailureEvidence(),
		)
	}
	evidence.ClientHTTPStatus = http.StatusUpgradeRequired
	return evidence, evidence.validate()
}

func runHeldIngressPreflight(
	ctx context.Context,
	config config,
	generation *daemonGeneration,
	audit *exchangeAuditReader,
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
	activityBaseline, err := audit.latestSequence(ctx)
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
	const prompt = "Reply exactly VIBEMATE_PREFLIGHT and nothing else."
	var (
		run    *agentRun
		queued offlinehold.Snapshot
	)
	if config.clientID == acceptanceClientCodexCLI {
		run, err = startFallbackAgent(
			config,
			workingDirectory,
			prompt,
			"",
		)
	} else {
		run, queued, err = queueDeferredClaudeProviderRequest(
			ctx,
			config,
			generation,
			workingDirectory,
			prompt,
			"",
		)
	}
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
	egressKind, err := heldPreflightEgressKind(config.clientID)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	if config.clientID == acceptanceClientCodexCLI {
		queued, err = waitForHeldQueuedEgressKind(
			ctx,
			generation.control,
			run,
			egressKind,
		)
		if err != nil {
			return ingressPreflightEvidence{}, err
		}
	}
	if err := requireSingleHeldQueuedEgress(queued, egressKind); err != nil {
		return ingressPreflightEvidence{}, err
	}
	fallbackEvidence := codexHTTPFallbackEvidence{}
	if config.clientID == acceptanceClientCodexCLI {
		fallbackContext, cancelFallback := context.WithTimeout(
			ctx,
			10*time.Second,
		)
		err = waitForResponsesHTTPFallbackAudit(
			fallbackContext,
			generation.control,
			config,
			connectionBaseline,
		)
		cancelFallback()
		if err != nil {
			return ingressPreflightEvidence{}, err
		}
		fallbackEvidence.ConnectionAudit = true
	}
	queuedKinds, err := queuedKindSummary(queued)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	releasing, err := generation.control.offlineAction(
		ctx,
		"resume",
		queued.Revision,
	)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	if err := requireSingleReleasedEgress(releasing, egressKind); err != nil {
		return ingressPreflightEvidence{}, err
	}
	failure, err := waitForExchangeFailureAfter(
		ctx,
		audit,
		config.accessID,
		activityBaseline,
		exchange.ReasonProviderCredentialUnavailable,
		45*time.Second,
	)
	if err != nil {
		return ingressPreflightEvidence{}, err
	}
	if config.clientID == acceptanceClientCodexCLI {
		fallbackEvidence.RuntimeReason = exchange.ReasonCode(failure.ReasonCode)
		fallbackContext, cancelFallback := context.WithTimeout(
			ctx,
			10*time.Second,
		)
		fallbackEvidence, err = completeCodexHTTPFallbackEvidence(
			fallbackContext,
			run,
			fallbackEvidence,
		)
		cancelFallback()
		if err != nil {
			return ingressPreflightEvidence{}, err
		}
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
	if err := requireOnlyExchangeFailureAfter(
		ctx,
		audit,
		config.accessID,
		activityBaseline,
		exchange.ReasonProviderCredentialUnavailable,
		failure,
	); err != nil {
		return ingressPreflightEvidence{}, err
	}
	if err := requireCanonicalExchangeSummary(
		ctx,
		generation.control,
		failure,
	); err != nil {
		return ingressPreflightEvidence{}, err
	}
	return ingressPreflightEvidence{
		queuedKinds:        queuedKinds,
		providerReason:     failure.ReasonCode,
		providerFailure:    failure,
		connectionBaseline: connectionBaseline,
		codexHTTPFallback:  fallbackEvidence,
	}, nil
}

func queueDeferredClaudeProviderRequest(
	ctx context.Context,
	config config,
	generation *daemonGeneration,
	workingDirectory string,
	prompt string,
	marker string,
) (*agentRun, offlinehold.Snapshot, error) {
	if ctx == nil ||
		generation == nil ||
		generation.control == nil ||
		config.clientID != acceptanceClientClaudeCode {
		return nil, offlinehold.Snapshot{}, errors.New(
			"deferred Claude provider request dependencies are invalid",
		)
	}
	initial, err := generation.control.offline(ctx)
	if err != nil {
		return nil, offlinehold.Snapshot{}, err
	}
	if err := requireCleanHeldEgress(initial); err != nil {
		return nil, offlinehold.Snapshot{}, err
	}
	run, input, err := startDeferredClaudeAgent(
		config,
		workingDirectory,
		"",
		marker,
	)
	if err != nil {
		return nil, offlinehold.Snapshot{}, err
	}
	cleanup := func(root error) (*agentRun, offlinehold.Snapshot, error) {
		_ = input.close()
		interruptErr := run.signalInterrupt()
		if interruptErr != nil && !errors.Is(interruptErr, os.ErrProcessDone) {
			root = errors.Join(root, interruptErr)
		}
		waitContext, cancelWait := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		_, waitErr := run.wait(waitContext)
		cancelWait()
		return nil, offlinehold.Snapshot{}, errors.Join(root, waitErr)
	}
	startup, err := waitForClaudeStartupControlEgress(
		ctx,
		generation.control,
		run,
	)
	if err != nil {
		return cleanup(err)
	}
	releasing, err := generation.control.offlineAction(
		ctx,
		"resume",
		startup.Revision,
	)
	if err != nil {
		return cleanup(err)
	}
	if err := requireClaudeStartupControlRelease(releasing); err != nil {
		return cleanup(err)
	}
	if err := waitForOfflineSettlementOrAgentExit(
		ctx,
		generation.control,
		run,
		45*time.Second,
		"deferred-input startup",
	); err != nil {
		return cleanup(err)
	}
	current, err := generation.control.offline(ctx)
	if err != nil {
		return cleanup(err)
	}
	held, err := generation.control.offlineAction(
		ctx,
		"enter",
		current.Revision,
	)
	if err != nil {
		return cleanup(err)
	}
	if err := requireCleanHeldEgress(held); err != nil {
		return cleanup(err)
	}
	if err := input.sendClaudePrompt(prompt); err != nil {
		return cleanup(err)
	}
	queued, err := waitForHeldQueuedEgressKind(
		ctx,
		generation.control,
		run,
		offlinehold.EgressProvider,
	)
	if err != nil {
		return cleanup(err)
	}
	return run, queued, nil
}

func requireCleanHeldEgress(snapshot offlinehold.Snapshot) error {
	if snapshot.State != offlinehold.StateHeld ||
		!snapshot.SafeToDisconnect ||
		snapshot.ActiveActions != 0 ||
		snapshot.EnteringActions != 0 ||
		snapshot.ActiveEgress != 0 ||
		snapshot.QueuedRequests != 0 ||
		snapshot.HeldBytes != 0 ||
		len(snapshot.ActiveByKind) != 0 ||
		len(snapshot.QueuedByKind) != 0 {
		return fmt.Errorf(
			"offline hold was not empty before one client request: %+v",
			snapshot,
		)
	}
	return nil
}

func waitForClaudeStartupControlEgress(
	ctx context.Context,
	control *controlClient,
	run *agentRun,
) (offlinehold.Snapshot, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var stableSince time.Time
	for {
		snapshot, err := control.offline(ctx)
		if err != nil {
			return offlinehold.Snapshot{}, err
		}
		if err := requireClaudeStartupControlEgress(snapshot, false); err != nil {
			return offlinehold.Snapshot{}, err
		}
		if snapshot.QueuedRequests == claudeStartupControlRequestCount {
			if stableSince.IsZero() {
				stableSince = time.Now()
			} else if time.Since(stableSince) >= claudeStartupQueueStableFor {
				if err := requireClaudeStartupControlEgress(
					snapshot,
					true,
				); err != nil {
					return offlinehold.Snapshot{}, err
				}
				return snapshot, nil
			}
		} else {
			stableSince = time.Time{}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return offlinehold.Snapshot{}, ctx.Err()
		case <-run.done:
			return offlinehold.Snapshot{}, agentExitedBeforeHeldEgress(
				ctx,
				run,
				offlinehold.EgressOpaque,
			)
		}
	}
}

func requireClaudeStartupControlEgress(
	snapshot offlinehold.Snapshot,
	requireComplete bool,
) error {
	opaque := snapshot.QueuedByKind[offlinehold.EgressOpaque]
	if snapshot.State != offlinehold.StateHeld ||
		!snapshot.SafeToDisconnect ||
		snapshot.ActiveEgress != 0 ||
		snapshot.EnteringActions != 0 ||
		snapshot.HeldBytes != 0 ||
		snapshot.QueuedRequests < 0 ||
		snapshot.QueuedRequests > claudeStartupControlRequestCount ||
		opaque != snapshot.QueuedRequests ||
		snapshot.ActiveActions < snapshot.QueuedRequests ||
		snapshot.ActiveActions > claudeStartupControlRequestCount ||
		(requireComplete &&
			(snapshot.QueuedRequests != claudeStartupControlRequestCount ||
				snapshot.ActiveActions != claudeStartupControlRequestCount)) {
		return fmt.Errorf(
			"fixed Claude startup did not isolate its exact bodyless control pair: %+v",
			snapshot,
		)
	}
	for kind, count := range snapshot.QueuedByKind {
		if count < 0 || (count != 0 && kind != offlinehold.EgressOpaque) {
			return fmt.Errorf(
				"fixed Claude startup held an unexpected %s egress: %+v",
				kind,
				snapshot,
			)
		}
	}
	if len(snapshot.ActiveByKind) != 0 {
		return fmt.Errorf(
			"fixed Claude startup had active egress while held: %+v",
			snapshot,
		)
	}
	return nil
}

func requireClaudeStartupControlRelease(
	snapshot offlinehold.Snapshot,
) error {
	if snapshot.State != offlinehold.StateReleasing ||
		snapshot.ActiveActions != claudeStartupControlRequestCount ||
		snapshot.EnteringActions != 0 ||
		snapshot.ActiveEgress != claudeStartupControlRequestCount ||
		snapshot.ActiveByKind[offlinehold.EgressOpaque] != claudeStartupControlRequestCount ||
		snapshot.QueuedRequests != 0 ||
		len(snapshot.QueuedByKind) != 0 ||
		snapshot.HeldBytes != 0 {
		return fmt.Errorf(
			"fixed Claude startup control pair was not released exactly: %+v",
			snapshot,
		)
	}
	return nil
}

func waitForOfflineSettlementOrAgentExit(
	ctx context.Context,
	control *controlClient,
	run *agentRun,
	limit time.Duration,
	phase string,
) error {
	waitContext, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
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
				"%s did not settle before deadline: %+v: %w",
				phase,
				latest,
				waitContext.Err(),
			)
		case <-run.done:
			exitCode, waitErr := run.wait(waitContext)
			return fmt.Errorf(
				"fixed client exited during %s: %w",
				phase,
				agentProcessFailure(phase, exitCode, waitErr, run),
			)
		}
	}
}

func heldPreflightEgressKind(
	clientID acceptanceClientID,
) (offlinehold.EgressKind, error) {
	switch clientID {
	case acceptanceClientClaudeCode:
		return offlinehold.EgressProvider, nil
	case acceptanceClientCodexCLI:
		return offlinehold.EgressProvider, nil
	default:
		return "", errors.New(
			"fixed client preflight egress boundary is unsupported",
		)
	}
}

func requireSingleHeldQueuedEgress(
	snapshot offlinehold.Snapshot,
	kind offlinehold.EgressKind,
) error {
	if snapshot.State != offlinehold.StateHeld ||
		!snapshot.SafeToDisconnect ||
		snapshot.ActiveEgress != 0 ||
		snapshot.QueuedRequests != 1 ||
		snapshot.HeldBytes <= 0 ||
		snapshot.QueuedByKind[kind] != 1 {
		return fmt.Errorf(
			"preflight did not isolate one held %s egress: %+v",
			kind,
			snapshot,
		)
	}
	for observedKind, count := range snapshot.QueuedByKind {
		if count < 0 || (count != 0 && observedKind != kind) {
			return fmt.Errorf(
				"preflight held an unexpected %s egress: %+v",
				observedKind,
				snapshot,
			)
		}
	}
	return nil
}

func requireSingleReleasedEgress(
	snapshot offlinehold.Snapshot,
	kind offlinehold.EgressKind,
) error {
	if snapshot.State != offlinehold.StateReleasing ||
		snapshot.QueuedRequests != 0 ||
		snapshot.ActiveEgress != 1 ||
		snapshot.ActiveByKind[kind] != 1 {
		return fmt.Errorf(
			"offline resume did not isolate one released %s egress: %+v",
			kind,
			snapshot,
		)
	}
	for observedKind, count := range snapshot.ActiveByKind {
		if count < 0 || (count != 0 && observedKind != kind) {
			return fmt.Errorf(
				"offline resume released an unexpected %s egress: %+v",
				observedKind,
				snapshot,
			)
		}
	}
	return nil
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
	const prompt = "Reply exactly VIBEMATE_KILLED_REQUEST and nothing else."
	var (
		run    *agentRun
		queued offlinehold.Snapshot
	)
	if config.clientID == acceptanceClientClaudeCode {
		run, queued, err = queueDeferredClaudeProviderRequest(
			ctx,
			config,
			generation,
			workingDirectory,
			prompt,
			"VIBEMATE_KILLED_REQUEST",
		)
	} else {
		run, err = startAgent(
			config,
			workingDirectory,
			prompt,
			"",
			"VIBEMATE_KILLED_REQUEST",
		)
	}
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
	if config.clientID != acceptanceClientClaudeCode {
		if err := waitForQueuedEgress(ctx, generation.control, run); err != nil {
			return cleanup(err)
		}
		queued, err = generation.control.offline(ctx)
		if err != nil {
			return cleanup(err)
		}
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
		config,
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
	audit *exchangeAuditReader,
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
			audit,
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
	if err := requireSuccessfulAgentEvidence(config.clientID, run); err != nil {
		return fmt.Errorf("normal reply evidence: %w", err)
	}
	return waitForOfflineSettlement(ctx, generation.control, 10*time.Second)
}

func requireSuccessfulAgentEvidence(
	clientID acceptanceClientID,
	run *agentRun,
) error {
	_, deltas, _, marker := run.evidence()
	if !marker {
		return errors.New("assistant completion marker was not observed")
	}
	switch clientID {
	case acceptanceClientClaudeCode:
		if deltas == 0 {
			return errors.New("Claude emitted no streamed content delta")
		}
	case acceptanceClientCodexCLI:
		evidence := run.protocolEvidence()
		if evidence.ThreadID.String() == "" ||
			evidence.AgentMessages == 0 ||
			!evidence.TurnStarted ||
			!evidence.TurnCompleted {
			return fmt.Errorf(
				"Codex JSONL completion is incomplete: messages=%d started=%t completed=%t",
				evidence.AgentMessages,
				evidence.TurnStarted,
				evidence.TurnCompleted,
			)
		}
	default:
		return errors.New("fixed client completion evidence is unsupported")
	}
	return nil
}

func latestExchangeFailure(
	ctx context.Context,
	audit *exchangeAuditReader,
	accessID string,
) (string, error) {
	record, exists, err := audit.latestFailure(ctx, accessID)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", nil
	}
	return record.ReasonCode, nil
}

func successfulExchangeSubjectsAfter(
	records []exchangeAuditRecord,
	accessID string,
	after int64,
) []string {
	matches := make([]exchangeAuditRecord, 0, len(records))
	for _, record := range records {
		if record.Sequence <= after ||
			record.AccessID != accessID ||
			record.Status != activity.StatusSucceeded ||
			record.ExchangeID == "" {
			continue
		}
		matches = append(matches, record)
	}
	slices.SortFunc(matches, func(left, right exchangeAuditRecord) int {
		switch {
		case left.Sequence < right.Sequence:
			return -1
		case left.Sequence > right.Sequence:
			return 1
		default:
			return strings.Compare(left.ExchangeID, right.ExchangeID)
		}
	})
	seen := make(map[string]struct{}, len(matches))
	subjects := make([]string, 0, len(matches))
	for _, record := range matches {
		if _, duplicate := seen[record.ExchangeID]; duplicate {
			continue
		}
		seen[record.ExchangeID] = struct{}{}
		subjects = append(subjects, record.ExchangeID)
	}
	return subjects
}

func waitForSuccessfulExchangesAfter(
	ctx context.Context,
	audit *exchangeAuditReader,
	control *controlClient,
	accessID string,
	after int64,
	expected int,
	limit time.Duration,
) error {
	if after < 0 || expected <= 0 {
		return errors.New("successful Exchange evidence request is invalid")
	}
	waitContext, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	observed := 0
	for {
		records, err := audit.terminalsAfter(
			waitContext,
			accessID,
			after,
		)
		if err != nil {
			return err
		}
		observed = len(successfulExchangeSubjectsAfter(
			records,
			accessID,
			after,
		))
		if len(records) > observed {
			return errors.New(
				"post-baseline Codex resume produced a non-successful or duplicate Exchange",
			)
		}
		if observed > expected {
			return fmt.Errorf(
				"successful Exchange evidence count=%d exceeded=%d",
				observed,
				expected,
			)
		}
		if observed == expected {
			for _, record := range records {
				if err := requireCanonicalExchangeSummary(
					waitContext,
					control,
					record,
				); err != nil {
					return err
				}
			}
			return nil
		}
		select {
		case <-ticker.C:
		case <-waitContext.Done():
			return fmt.Errorf(
				"successful Exchange evidence count=%d want=%d: %w",
				observed,
				expected,
				waitContext.Err(),
			)
		}
	}
}

func runCodexResume(
	ctx context.Context,
	config config,
	generation *daemonGeneration,
	audit *exchangeAuditReader,
) error {
	if config.clientID != acceptanceClientCodexCLI {
		return errors.New("Codex resume requires the fixed Codex client")
	}
	activityBaseline, err := audit.latestSequence(ctx)
	if err != nil {
		return err
	}
	workingDirectory, err := os.MkdirTemp(
		"",
		"vibermate-codex-resume-*",
	)
	if err != nil {
		return err
	}
	defer os.RemoveAll(workingDirectory)

	first, err := startAgent(
		config,
		workingDirectory,
		"Reply exactly VIBEMATE_RESUME_FIRST and nothing else.",
		"",
		"VIBEMATE_RESUME_FIRST",
	)
	if err != nil {
		return err
	}
	firstContext, cancelFirst := context.WithTimeout(ctx, 3*time.Minute)
	firstExit, firstErr := first.wait(firstContext)
	cancelFirst()
	if firstErr != nil || firstExit != 0 {
		return agentProcessFailure(
			"resume initial turn",
			firstExit,
			firstErr,
			first,
		)
	}
	if err := requireSuccessfulAgentEvidence(
		config.clientID,
		first,
	); err != nil {
		return err
	}
	threadID := first.protocolEvidence().ThreadID
	if threadID.String() == "" {
		return errors.New("Codex initial turn omitted its thread identity")
	}

	resumed, err := startResumeAgent(
		config,
		workingDirectory,
		"Reply exactly VIBEMATE_RESUME_SECOND and nothing else.",
		threadID,
		"VIBEMATE_RESUME_SECOND",
	)
	if err != nil {
		return err
	}
	resumeContext, cancelResume := context.WithTimeout(ctx, 3*time.Minute)
	resumeExit, resumeErr := resumed.wait(resumeContext)
	cancelResume()
	if resumeErr != nil || resumeExit != 0 {
		return agentProcessFailure(
			"resumed turn",
			resumeExit,
			resumeErr,
			resumed,
		)
	}
	if err := requireSuccessfulAgentEvidence(
		config.clientID,
		resumed,
	); err != nil {
		return err
	}
	if resumed.protocolEvidence().ThreadID != threadID {
		return errors.New("Codex exec resume changed thread identity")
	}
	return waitForSuccessfulExchangesAfter(
		ctx,
		audit,
		generation.control,
		config.accessID,
		activityBaseline,
		2,
		15*time.Second,
	)
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

const maxResponsesFallbackConnectionBytes = 64 << 10

func waitForResponsesHTTPFallbackAudit(
	ctx context.Context,
	control *controlClient,
	config config,
	after int64,
) error {
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return err
	}
	if client.ID != acceptanceClientCodexCLI {
		return errors.New("Responses HTTP fallback requires fixed Codex")
	}
	clientOrigin, err := access.NewClientOrigin(client.ClientOrigin)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, err := connectionRecordsAfter(ctx, control, after)
		if err != nil {
			return err
		}
		ready, err := responsesHTTPFallbackAuditReady(
			records,
			clientOrigin,
		)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func responsesHTTPFallbackAuditReady(
	records []connectionevent.Record,
	clientOrigin access.ClientOrigin,
) (bool, error) {
	authority := clientOrigin.EndpointAuthority()
	host := clientOrigin.TLSServerName()
	if authority == "" || host == "" {
		return false, errors.New("fallback client origin is invalid")
	}
	timelines := make(map[string][]connectionevent.Record)
	for _, record := range records {
		if record.RequestedHost != host {
			continue
		}
		if err := record.Validate(); err != nil {
			return false, fmt.Errorf(
				"validate fallback ConnectionEvent: %w",
				err,
			)
		}
		timelines[record.ConnectionID] = append(
			timelines[record.ConnectionID],
			record,
		)
	}
	if len(timelines) < 2 {
		return false, nil
	}
	if len(timelines) > 2 {
		return false, errors.New(
			"fixed Codex fallback opened unexpected client connections",
		)
	}

	var negotiation *connectionevent.Record
	var activeHTTP *connectionevent.Record
	for _, timeline := range timelines {
		slices.SortFunc(
			timeline,
			func(left, right connectionevent.Record) int {
				switch {
				case left.Sequence < right.Sequence:
					return -1
				case left.Sequence > right.Sequence:
					return 1
				default:
					return 0
				}
			},
		)
		latest := timeline[len(timeline)-1]
		switch latest.Phase {
		case connectionevent.PhaseAttempted,
			connectionevent.PhaseDecided:
			return false, nil
		case connectionevent.PhaseConnected:
			if !connectionPhaseSequence(
				timeline,
				[]connectionevent.Phase{
					connectionevent.PhaseAttempted,
					connectionevent.PhaseDecided,
					connectionevent.PhaseConnected,
				},
			) {
				return false, errors.New(
					"fallback HTTP connection timeline is invalid",
				)
			}
			candidate := latest
			activeHTTP = &candidate
		case connectionevent.PhaseClosed:
			if !connectionPhaseSequence(
				timeline,
				[]connectionevent.Phase{
					connectionevent.PhaseAttempted,
					connectionevent.PhaseDecided,
					connectionevent.PhaseConnected,
					connectionevent.PhaseClosed,
				},
			) {
				return false, errors.New(
					"fallback negotiation timeline is invalid",
				)
			}
			candidate := latest
			negotiation = &candidate
		default:
			return false, errors.New(
				"fixed Codex fallback connection terminated unexpectedly",
			)
		}
	}
	if negotiation == nil || activeHTTP == nil {
		return false, nil
	}
	for label, record := range map[string]*connectionevent.Record{
		"negotiation": negotiation,
		"HTTP":        activeHTTP,
	} {
		if record.IngressID == "" ||
			record.SourceLabel == "" ||
			record.SourceConfidence !=
				connectionevent.SourceConfidenceVerified ||
			record.Decision != connectionevent.DecisionAllow ||
			record.Decryption != connectionevent.DecryptionMITM ||
			record.ObservedSNI != host ||
			record.RouteHost != host ||
			record.CredentialBindingID != "" {
			return false, fmt.Errorf(
				"fallback %s connection evidence is invalid",
				label,
			)
		}
	}
	if negotiation.IngressID != activeHTTP.IngressID ||
		negotiation.SourceLabel != activeHTTP.SourceLabel ||
		negotiation.Sequence >= activeHTTP.Sequence {
		return false, errors.New(
			"fallback connections do not share one CaptureRun",
		)
	}
	if negotiation.Outcome != connectionevent.OutcomeCompleted ||
		negotiation.ErrorClass != "" ||
		negotiation.BytesUp == 0 ||
		negotiation.BytesDown == 0 ||
		negotiation.BytesUp > maxResponsesFallbackConnectionBytes ||
		negotiation.BytesDown > maxResponsesFallbackConnectionBytes {
		return false, errors.New(
			"fallback negotiation was not bounded and completed",
		)
	}
	if activeHTTP.Outcome != "" ||
		activeHTTP.ErrorClass != "" ||
		activeHTTP.BytesUp != 0 ||
		activeHTTP.BytesDown != 0 {
		return false, errors.New(
			"fallback HTTP Exchange connection is not active",
		)
	}
	return true, nil
}

func connectionPhaseSequence(
	records []connectionevent.Record,
	expected []connectionevent.Phase,
) bool {
	if len(records) != len(expected) {
		return false
	}
	for index, phase := range expected {
		if records[index].Phase != phase {
			return false
		}
	}
	return true
}

func waitForClientConnectionAudit(
	ctx context.Context,
	control *controlClient,
	config config,
	after int64,
	limit time.Duration,
) (string, error) {
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return "", err
	}
	clientOrigin, err := access.NewClientOrigin(client.ClientOrigin)
	if err != nil {
		return "", err
	}
	waitContext, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		records, err := connectionRecordsAfter(waitContext, control, after)
		if err != nil {
			if waitContext.Err() != nil {
				if lastErr != nil {
					return "", lastErr
				}
				return "", fmt.Errorf(
					"client ConnectionEvent evidence did not become terminal: %w",
					waitContext.Err(),
				)
			}
			return "", err
		}
		seen := make(map[string]struct{})
		for _, record := range records {
			if record.RequestedHost != clientOrigin.TLSServerName() {
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
			ready, err := clientConnectionAuditReady(
				timeline,
				clientOrigin,
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
				"client ConnectionEvent evidence did not become terminal: %w",
				waitContext.Err(),
			)
		}
	}
}

func clientConnectionAuditReady(
	timeline connectionevent.Timeline,
	clientOrigin access.ClientOrigin,
) (bool, error) {
	if len(timeline.Events) == 0 {
		return false, errors.New("client ConnectionEvent timeline is empty")
	}
	if !connectionPhaseSequence(
		timeline.Events,
		[]connectionevent.Phase{
			connectionevent.PhaseAttempted,
			connectionevent.PhaseDecided,
			connectionevent.PhaseConnected,
			connectionevent.PhaseClosed,
		},
	) {
		last := timeline.Events[len(timeline.Events)-1]
		if !connectionRecordTerminal(last) {
			return false, nil
		}
		return false, errors.New(
			"client ConnectionEvent phase sequence is invalid",
		)
	}
	if err := validateClientConnectionAudit(
		timeline,
		clientOrigin,
	); err != nil {
		return false, err
	}
	last := timeline.Events[len(timeline.Events)-1]
	if last.Outcome != connectionevent.OutcomeCompleted ||
		last.ErrorClass != "" ||
		last.BytesUp == 0 ||
		last.BytesDown == 0 {
		return false, fmt.Errorf(
			"client ConnectionEvent terminal evidence is invalid: %+v",
			last,
		)
	}
	return true, nil
}

func activeClientConnectionAuditReady(
	timeline connectionevent.Timeline,
	clientOrigin access.ClientOrigin,
) (bool, error) {
	if len(timeline.Events) == 0 {
		return false, nil
	}
	last := timeline.Events[len(timeline.Events)-1]
	if connectionRecordTerminal(last) {
		return false, nil
	}
	if len(timeline.Events) < 3 {
		return false, nil
	}
	if !connectionPhaseSequence(
		timeline.Events,
		[]connectionevent.Phase{
			connectionevent.PhaseAttempted,
			connectionevent.PhaseDecided,
			connectionevent.PhaseConnected,
		},
	) {
		return false, errors.New(
			"active client ConnectionEvent phase sequence is invalid",
		)
	}
	if err := validateClientConnectionAudit(
		timeline,
		clientOrigin,
	); err != nil {
		return false, err
	}
	return true, nil
}

func validateClientConnectionAudit(
	timeline connectionevent.Timeline,
	clientOrigin access.ClientOrigin,
) error {
	if len(timeline.Events) == 0 {
		return errors.New("client ConnectionEvent timeline is empty")
	}
	first := timeline.Events[0]
	if first.ConnectionID != timeline.ConnectionID ||
		first.Phase != connectionevent.PhaseAttempted ||
		first.SourceConfidence != connectionevent.SourceConfidenceUnknown ||
		first.IngressID != "" ||
		first.SourceLabel != "" ||
		first.RequestedHost != clientOrigin.TLSServerName() ||
		first.Port != clientOrigin.Port() ||
		first.ObservedSNI != "" ||
		first.RouteHost != "" ||
		first.Decision != "" ||
		first.RuleID != "" ||
		first.CredentialBindingID != "" ||
		first.EgressScope != "" ||
		first.EgressSource != "" ||
		first.EgressRuleID != "" ||
		first.EgressSelectorRunID != "" ||
		first.EgressProxyID != "" ||
		first.EgressPolicyRevision != 0 ||
		first.Decryption != connectionevent.DecryptionNone {
		return fmt.Errorf(
			"client ConnectionEvent attempt is invalid: %+v",
			first,
		)
	}
	expectedHost := clientOrigin.TLSServerName()
	expectedIngress := ""
	expectedSource := ""
	previousSequence := int64(0)
	for index, record := range timeline.Events {
		if err := record.Validate(); err != nil {
			return err
		}
		if record.ConnectionID != timeline.ConnectionID ||
			record.RequestedHost != expectedHost ||
			record.Port != clientOrigin.Port() ||
			record.Sequence <= previousSequence {
			return errors.New(
				"client ConnectionEvent identity or ordering changed",
			)
		}
		previousSequence = record.Sequence
		if index == 0 {
			continue
		}
		if record.SourceConfidence != connectionevent.SourceConfidenceVerified ||
			record.IngressID == "" ||
			record.SourceLabel == "" ||
			record.Decision != connectionevent.DecisionAllow ||
			record.RuleID != acceptanceConnectionRuleID ||
			record.RouteHost != expectedHost ||
			record.CredentialBindingID != "" ||
			record.EgressScope != connectionevent.EgressScopeAccess ||
			record.EgressSource != connectionevent.EgressSourceAccessDefault ||
			record.EgressRuleID != "" ||
			record.EgressSelectorRunID != "" ||
			record.EgressProxyID != "" ||
			record.EgressPolicyRevision != 1 ||
			record.Decryption != connectionevent.DecryptionMITM {
			return fmt.Errorf(
				"client ConnectionEvent allow evidence is invalid: %+v",
				record,
			)
		}
		if expectedIngress == "" {
			expectedIngress = record.IngressID
			expectedSource = record.SourceLabel
		} else if record.IngressID != expectedIngress ||
			record.SourceLabel != expectedSource {
			return errors.New(
				"client ConnectionEvent source identity changed",
			)
		}
		if index == 1 && record.ObservedSNI != "" {
			return errors.New(
				"client ConnectionEvent observed SNI before TLS handshake",
			)
		}
		if index >= 2 && record.ObservedSNI != expectedHost {
			return errors.New(
				"client ConnectionEvent omitted the observed SNI",
			)
		}
	}
	return nil
}

func waitForActiveConnectionAudit(
	ctx context.Context,
	control *controlClient,
	config config,
	after int64,
	limit time.Duration,
) (string, error) {
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return "", err
	}
	clientOrigin, err := access.NewClientOrigin(client.ClientOrigin)
	if err != nil {
		return "", err
	}
	waitContext, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		records, err := connectionRecordsAfter(waitContext, control, after)
		if err != nil {
			if waitContext.Err() != nil {
				if lastErr != nil {
					return "", lastErr
				}
				return "", fmt.Errorf(
					"active held ConnectionEvent was not observed: %w",
					waitContext.Err(),
				)
			}
			return "", err
		}
		seen := make(map[string]struct{})
		for _, record := range records {
			if record.RequestedHost != clientOrigin.TLSServerName() {
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
			if last.Sequence <= after {
				continue
			}
			ready, err := activeClientConnectionAuditReady(
				timeline,
				clientOrigin,
			)
			if err != nil {
				lastErr = err
				continue
			}
			if ready {
				return timeline.ConnectionID, nil
			}
		}
		select {
		case <-ticker.C:
		case <-waitContext.Done():
			if lastErr != nil {
				return "", lastErr
			}
			return "", fmt.Errorf(
				"active held ConnectionEvent was not observed: %w",
				waitContext.Err(),
			)
		}
	}
}

type offlineSnapshotReader interface {
	offline(context.Context) (offlinehold.Snapshot, error)
}

func waitForActiveProviderEgress(
	ctx context.Context,
	reader offlineSnapshotReader,
	run *agentRun,
	limit time.Duration,
) error {
	if ctx == nil || reader == nil || run == nil || limit <= 0 {
		return errors.New("active provider egress observation is incomplete")
	}
	waitContext, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err := reader.offline(waitContext)
		if err != nil {
			return err
		}
		if snapshot.State == offlinehold.StateOnline &&
			snapshot.ActiveActions > 0 &&
			snapshot.ActiveEgress > 0 &&
			snapshot.ActiveByKind[offlinehold.EgressProvider] > 0 {
			return nil
		}
		select {
		case <-ticker.C:
		case <-waitContext.Done():
			return fmt.Errorf(
				"active provider egress was not observed: %w",
				waitContext.Err(),
			)
		case <-run.done:
			exitCode, waitErr := run.wait(waitContext)
			return fmt.Errorf(
				"%s exited before provider egress became active: %w",
				run.clientLabel(),
				agentProcessFailure(
					"interrupt precondition",
					exitCode,
					waitErr,
					run,
				),
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
	audit *exchangeAuditReader,
	accessID string,
	after int64,
	expected exchange.ReasonCode,
	limit time.Duration,
) (exchangeAuditRecord, error) {
	waitContext, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, err := audit.terminalsAfter(
			waitContext,
			accessID,
			after,
		)
		if err != nil {
			return exchangeAuditRecord{}, err
		}
		latest, exists, err := singleExpectedExchangeFailure(
			records,
			accessID,
			expected,
		)
		if err != nil {
			return exchangeAuditRecord{}, err
		}
		if exists {
			return latest, nil
		}
		select {
		case <-ticker.C:
		case <-waitContext.Done():
			return exchangeAuditRecord{}, fmt.Errorf(
				"expected Exchange failure was not recorded: %w",
				waitContext.Err(),
			)
		}
	}
}

func singleExpectedExchangeFailure(
	records []exchangeAuditRecord,
	accessID string,
	expected exchange.ReasonCode,
) (exchangeAuditRecord, bool, error) {
	if accessID == "" || expected == "" {
		return exchangeAuditRecord{}, false, errors.New(
			"expected Exchange failure is incomplete",
		)
	}
	if len(records) == 0 {
		return exchangeAuditRecord{}, false, nil
	}
	if len(records) != 1 {
		return exchangeAuditRecord{}, false, fmt.Errorf(
			"post-baseline Exchange terminal count=%d, want=1",
			len(records),
		)
	}
	record := records[0]
	if record.AccessID != accessID ||
		record.Status != activity.StatusFailed ||
		record.ReasonCode != string(expected) {
		return exchangeAuditRecord{}, false, fmt.Errorf(
			"deterministic provider Exchange is not the expected failure: %+v",
			record,
		)
	}
	return record, true, nil
}

func requireOnlyExchangeFailureAfter(
	ctx context.Context,
	audit *exchangeAuditReader,
	accessID string,
	after int64,
	expectedReason exchange.ReasonCode,
	expectedRecord exchangeAuditRecord,
) error {
	records, err := audit.terminalsAfter(ctx, accessID, after)
	if err != nil {
		return err
	}
	record, exists, err := singleExpectedExchangeFailure(
		records,
		accessID,
		expectedReason,
	)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("post-baseline Exchange failure disappeared")
	}
	if record != expectedRecord {
		return errors.New("post-baseline Exchange failure changed after settlement")
	}
	return nil
}

type heldStreamingEvidence struct {
	ClientID     acceptanceClientID
	ClientDeltas int
	Completed    bool
}

func (evidence heldStreamingEvidence) reportDetail() (string, error) {
	if !evidence.Completed || evidence.ClientDeltas < 0 {
		return "", errors.New("held streaming evidence is incomplete")
	}
	switch evidence.ClientID {
	case acceptanceClientClaudeCode:
		if evidence.ClientDeltas < 2 {
			return "", errors.New(
				"Claude held streaming evidence requires multiple client deltas",
			)
		}
		return "held request resumed and returned multiple streamed client deltas", nil
	case acceptanceClientCodexCLI:
		return "held request resumed and completed through the Responses streaming path", nil
	default:
		return "", errors.New("held streaming report client is unsupported")
	}
}

func runHeldStreaming(
	ctx context.Context,
	config config,
	generation *daemonGeneration,
) (heldStreamingEvidence, error) {
	hold, err := generation.control.offline(ctx)
	if err != nil {
		return heldStreamingEvidence{}, err
	}
	hold, err = generation.control.offlineAction(ctx, "enter", hold.Revision)
	if err != nil {
		return heldStreamingEvidence{}, err
	}
	if hold.State != offlinehold.StateHeld || !hold.SafeToDisconnect {
		return heldStreamingEvidence{}, fmt.Errorf(
			"offline hold did not settle: %+v",
			hold,
		)
	}
	workingDirectory, err := os.MkdirTemp("", "vibermate-agent-stream-*")
	if err != nil {
		return heldStreamingEvidence{}, err
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
		return heldStreamingEvidence{}, err
	}
	if err := waitForQueuedEgress(ctx, generation.control, run); err != nil {
		return heldStreamingEvidence{}, err
	}
	current, err := generation.control.offline(ctx)
	if err != nil {
		return heldStreamingEvidence{}, err
	}
	resumed, err := generation.control.offlineAction(
		ctx,
		"resume",
		current.Revision,
	)
	if err != nil {
		return heldStreamingEvidence{}, err
	}
	if err := requireReleasedOfflineRequest(resumed); err != nil {
		return heldStreamingEvidence{}, err
	}
	waitContext, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	exitCode, err := run.wait(waitContext)
	if err != nil || exitCode != 0 {
		return heldStreamingEvidence{}, agentProcessFailure(
			"streaming",
			exitCode,
			err,
			run,
		)
	}
	if err := requireSuccessfulAgentEvidence(config.clientID, run); err != nil {
		return heldStreamingEvidence{}, fmt.Errorf(
			"held streaming evidence: %w",
			err,
		)
	}
	_, deltas, _, _ := run.evidence()
	evidence := heldStreamingEvidence{
		ClientID:     config.clientID,
		ClientDeltas: deltas,
		Completed:    true,
	}
	if _, err := evidence.reportDetail(); err != nil {
		return heldStreamingEvidence{}, err
	}
	if err := waitForOfflineSettlement(
		ctx,
		generation.control,
		10*time.Second,
	); err != nil {
		return heldStreamingEvidence{}, err
	}
	return evidence, nil
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
			return fmt.Errorf(
				"%s exited before egress queued in offline hold",
				run.clientLabel(),
			)
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
		if snapshot.QueuedByKind[kind] > 0 {
			if err := requireSingleHeldQueuedEgress(snapshot, kind); err != nil {
				return offlinehold.Snapshot{}, err
			}
			return snapshot, nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return offlinehold.Snapshot{}, ctx.Err()
		case <-run.done:
			return offlinehold.Snapshot{}, agentExitedBeforeHeldEgress(
				ctx,
				run,
				kind,
			)
		}
	}
}

func agentExitedBeforeHeldEgress(
	ctx context.Context,
	run *agentRun,
	kind offlinehold.EgressKind,
) error {
	exitCode, waitErr := run.wait(ctx)
	return fmt.Errorf(
		"%s exited before %s egress queued in offline hold: %w",
		run.clientLabel(),
		kind,
		agentProcessFailure("held-ingress", exitCode, waitErr, run),
	)
}

type toolApprovalEvidence struct {
	ClientID              acceptanceClientID
	ToolName              string
	Approved              bool
	Completed             bool
	InterruptedAfterProof bool
	DeniedFollowups       int
}

func (evidence toolApprovalEvidence) reportDetail() (string, error) {
	if !evidence.Approved {
		return "", errors.New("tool approval evidence is incomplete")
	}
	expected := ""
	switch evidence.ClientID {
	case acceptanceClientClaudeCode:
		expected = "Write"
		if !evidence.Completed || evidence.InterruptedAfterProof {
			return "", errors.New(
				"Claude tool approval evidence did not complete normally",
			)
		}
	case acceptanceClientCodexCLI:
		expected = "exec"
		if evidence.Completed || !evidence.InterruptedAfterProof {
			return "", errors.New(
				"Codex tool approval evidence was not bounded after proof",
			)
		}
	default:
		return "", errors.New("tool approval report client is unsupported")
	}
	if evidence.ToolName != expected {
		return "", errors.New(
			"tool approval evidence does not match the selected client",
		)
	}
	detail := evidence.ToolName +
		" remained behind the durable allow-once barrier and produced the bounded proof file"
	if evidence.InterruptedAfterProof {
		detail += " before the captured client was deliberately interrupted"
	} else {
		detail += " before the client completed normally"
	}
	if evidence.DeniedFollowups > 0 {
		detail += fmt.Sprintf(
			"; %d follow-up tool attempt(s) were separately denied",
			evidence.DeniedFollowups,
		)
	}
	return detail, nil
}

func runToolApproval(
	ctx context.Context,
	config config,
	generation *daemonGeneration,
) (toolApprovalEvidence, error) {
	workingDirectory, err := os.MkdirTemp("", "vibermate-agent-tool-*")
	if err != nil {
		return toolApprovalEvidence{}, err
	}
	defer os.RemoveAll(workingDirectory)
	spec, err := newToolApprovalSpec(
		config.clientID,
		workingDirectory,
	)
	if err != nil {
		return toolApprovalEvidence{}, err
	}
	baselinePage, err := generation.control.pendingApprovals(ctx)
	if err != nil {
		return toolApprovalEvidence{}, err
	}
	baselineApprovals := make(map[string]struct{}, len(baselinePage.Items))
	for _, approval := range baselinePage.Items {
		baselineApprovals[approval.ID] = struct{}{}
	}
	run, err := startAgent(
		config,
		workingDirectory,
		spec.prompt,
		spec.toolName,
		spec.marker,
	)
	if err != nil {
		return toolApprovalEvidence{}, err
	}
	if config.clientID == acceptanceClientClaudeCode {
		if err := run.waitForConfiguredTool(ctx, spec.toolName); err != nil {
			return toolApprovalEvidence{}, err
		}
	}
	if _, statErr := os.Stat(spec.proofPath); !errors.Is(
		statErr,
		os.ErrNotExist,
	) {
		if statErr == nil {
			return toolApprovalEvidence{}, errors.New(
				"tool side effect occurred before approval",
			)
		}
		return toolApprovalEvidence{}, fmt.Errorf(
			"inspect pre-approval tool proof: %w",
			statErr,
		)
	}
	approval, err := waitForToolApproval(
		ctx,
		generation.control,
		run,
		baselineApprovals,
		config.accessID,
		spec.toolName,
	)
	if err != nil {
		return toolApprovalEvidence{}, err
	}
	if len(approval.SubjectLabels) != 1 ||
		approval.SubjectLabels[0] != spec.toolName {
		return toolApprovalEvidence{}, fmt.Errorf(
			"unexpected approval tools: %v",
			approval.SubjectLabels,
		)
	}
	_, _, toolUsesBeforeDecision, markerBeforeDecision := run.evidence()
	if toolUsesBeforeDecision != 0 || markerBeforeDecision {
		return toolApprovalEvidence{}, fmt.Errorf(
			"tool stream leaked before approval: toolUses=%d marker=%t",
			toolUsesBeforeDecision,
			markerBeforeDecision,
		)
	}
	resolved, err := generation.control.allowOnce(ctx, approval)
	if err != nil {
		return toolApprovalEvidence{}, err
	}
	if resolved.State != toolapproval.StateAllowed {
		return toolApprovalEvidence{}, fmt.Errorf(
			"approval did not resolve allowed: %+v",
			resolved,
		)
	}
	completed := false
	interruptedAfterProof := false
	deniedFollowups := 0
	if config.clientID == acceptanceClientCodexCLI {
		waitContext, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err = waitForCodexApprovedToolProof(waitContext, run, spec)
		cancel()
		if err != nil {
			return toolApprovalEvidence{}, err
		}
		if err := run.signalInterrupt(); err != nil {
			return toolApprovalEvidence{}, err
		}
		interruptedAfterProof = true
		waitContext, cancel = context.WithTimeout(ctx, 20*time.Second)
		exitCode, waitErr := run.wait(waitContext)
		cancel()
		if waitErr != nil {
			return toolApprovalEvidence{}, agentProcessFailure(
				"tool post-proof interrupt",
				exitCode,
				waitErr,
				run,
			)
		}
	} else {
		knownApprovals := maps.Clone(baselineApprovals)
		knownApprovals[approval.ID] = struct{}{}
		waitContext, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		exitCode, followups, waitErr := waitForToolCompletion(
			waitContext,
			generation.control,
			run,
			knownApprovals,
			config.accessID,
			spec.toolName,
		)
		deniedFollowups = followups
		if waitErr != nil || exitCode != 0 {
			return toolApprovalEvidence{}, agentProcessFailure(
				"tool",
				exitCode,
				waitErr,
				run,
			)
		}
		completed = true
	}
	_, _, toolUses, marker := run.evidence()
	if toolUses != 1 || (completed && !marker) {
		return toolApprovalEvidence{}, fmt.Errorf(
			"tool approval evidence toolUses=%d marker=%t completed=%t",
			toolUses,
			marker,
			completed,
		)
	}
	if err := verifyToolApprovalProof(spec); err != nil {
		return toolApprovalEvidence{}, err
	}
	evidence := toolApprovalEvidence{
		ClientID:              config.clientID,
		ToolName:              spec.toolName,
		Approved:              true,
		Completed:             completed,
		InterruptedAfterProof: interruptedAfterProof,
		DeniedFollowups:       deniedFollowups,
	}
	if _, err := evidence.reportDetail(); err != nil {
		return toolApprovalEvidence{}, err
	}
	return evidence, nil
}

func waitForCodexApprovedToolProof(
	ctx context.Context,
	run *agentRun,
	spec toolApprovalSpec,
) error {
	if run == nil || run.clientID != acceptanceClientCodexCLI {
		return errors.New("Codex tool proof requires the fixed Codex client")
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, _, toolUses, _ := run.evidence()
		if toolUses > 1 {
			return fmt.Errorf(
				"Codex completed %d tools after one allow-once decision",
				toolUses,
			)
		}
		if toolUses == 1 {
			if err := verifyToolApprovalProof(spec); err != nil {
				return fmt.Errorf(
					"verify completed Codex tool proof: %w",
					err,
				)
			}
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		case <-run.done:
			exitCode, waitErr := run.wait(ctx)
			return agentProcessFailure(
				"tool before approved proof",
				exitCode,
				waitErr,
				run,
			)
		}
	}
}

type toolApprovalSpec struct {
	prompt       string
	toolName     string
	marker       string
	proofPath    string
	proofContent string
}

func newToolApprovalSpec(
	clientID acceptanceClientID,
	workingDirectory string,
) (toolApprovalSpec, error) {
	if !filepath.IsAbs(workingDirectory) {
		return toolApprovalSpec{}, errors.New(
			"tool approval working directory is not absolute",
		)
	}
	const (
		marker       = "VIBEMATE_TOOL_DONE"
		proofContent = "VIBEMATE_TOOL_APPROVAL_PROOF"
		toolOutput   = "VIBEMATE_TOOL_EXEC_OK"
	)
	toolName := ""
	proofPath := filepath.Join(
		filepath.Clean(workingDirectory),
		"approval-proof.txt",
	)
	var prompt string
	switch clientID {
	case acceptanceClientClaudeCode:
		toolName = "Write"
		prompt = fmt.Sprintf(
			"Use the %s tool exactly once to create %q with exactly this content: %q. "+
				"Do not use any other tool. After the tool succeeds, reply exactly %s.",
			toolName,
			proofPath,
			proofContent,
			marker,
		)
	case acceptanceClientCodexCLI:
		toolName = "exec"
		command := fmt.Sprintf(
			"printf %%s %s > approval-proof.txt && printf %%s %s",
			proofContent,
			toolOutput,
		)
		execSource := fmt.Sprintf(
			"const result = await tools.exec_command({cmd:%q,workdir:%q}); text(result.output);",
			command,
			filepath.Clean(workingDirectory),
		)
		prompt = fmt.Sprintf(
			"In working directory %q, the expected proof file is %q. "+
				"Use the %s tool exactly once with exactly this JavaScript source, "+
				"without Markdown fences: %s "+
				"Treat its output as success and do not call any tool again. "+
				"After it succeeds, reply exactly %s.",
			filepath.Clean(workingDirectory),
			proofPath,
			toolName,
			execSource,
			marker,
		)
	default:
		return toolApprovalSpec{}, errors.New(
			"tool approval client is unsupported",
		)
	}
	return toolApprovalSpec{
		prompt:       prompt,
		toolName:     toolName,
		marker:       marker,
		proofPath:    proofPath,
		proofContent: proofContent,
	}, nil
}

func verifyToolApprovalProof(spec toolApprovalSpec) error {
	info, err := os.Stat(spec.proofPath)
	if err != nil {
		return fmt.Errorf("inspect tool approval proof: %w", err)
	}
	if !info.Mode().IsRegular() ||
		info.Size() != int64(len(spec.proofContent)) {
		return errors.New("tool approval proof has an invalid shape")
	}
	content, err := os.ReadFile(spec.proofPath)
	if err != nil {
		return fmt.Errorf("read tool approval proof: %w", err)
	}
	if string(content) != spec.proofContent {
		return errors.New("tool approval proof content did not match")
	}
	return nil
}

func agentProcessFailure(
	label string,
	exitCode int,
	waitErr error,
	run *agentRun,
) error {
	message := fmt.Sprintf(
		"%s %s exit=%d",
		label,
		run.clientLabel(),
		exitCode,
	)
	if evidence := run.safeFailureEvidence(); evidence != "" {
		message += " " + evidence
	}
	if waitErr != nil {
		return fmt.Errorf("%s: %w", message, waitErr)
	}
	return errors.New(message)
}

func waitForToolApproval(
	ctx context.Context,
	control *controlClient,
	run *agentRun,
	baseline map[string]struct{},
	accessID string,
	toolName string,
) (toolapproval.View, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		page, err := control.pendingApprovals(ctx)
		if err != nil {
			return toolapproval.View{}, err
		}
		approval, found, err := selectToolApproval(
			page,
			baseline,
			accessID,
			toolName,
		)
		if err != nil {
			return toolapproval.View{}, err
		}
		if found {
			return approval, nil
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

func selectToolApproval(
	page toolapproval.Page,
	baseline map[string]struct{},
	accessID string,
	toolName string,
) (toolapproval.View, bool, error) {
	items := append([]toolapproval.View(nil), page.Items...)
	slices.SortFunc(items, func(left, right toolapproval.View) int {
		return left.CreatedAt.Compare(right.CreatedAt)
	})
	for _, approval := range items {
		if _, existed := baseline[approval.ID]; existed ||
			approval.Kind != string(toolapproval.KindToolIntent) ||
			approval.AccessID != accessID {
			continue
		}
		if approval.State != toolapproval.StatePending ||
			approval.ExchangeID == "" ||
			approval.PlanRevision == 0 ||
			len(approval.PlanHash) != 64 {
			return toolapproval.View{}, false, errors.New(
				"new tool approval is missing its active request binding",
			)
		}
		if len(approval.SubjectLabels) != 1 ||
			approval.SubjectLabels[0] != toolName {
			return toolapproval.View{}, false, fmt.Errorf(
				"new tool approval subjects are %v, want %q",
				approval.SubjectLabels,
				toolName,
			)
		}
		return approval, true, nil
	}
	return toolapproval.View{}, false, nil
}

func waitForToolCompletion(
	ctx context.Context,
	control *controlClient,
	run *agentRun,
	known map[string]struct{},
	accessID string,
	toolName string,
) (int, int, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	denied := 0
	for {
		page, err := control.pendingApprovals(ctx)
		if err != nil {
			return -1, denied, err
		}
		approval, found, err := selectToolApproval(
			page,
			known,
			accessID,
			toolName,
		)
		if err != nil {
			return -1, denied, err
		}
		if found {
			known[approval.ID] = struct{}{}
			resolved, denyErr := control.denyOnce(ctx, approval)
			if denyErr != nil {
				return -1, denied, denyErr
			}
			if resolved.State != toolapproval.StateDenied {
				return -1, denied, fmt.Errorf(
					"follow-up tool approval did not resolve denied: %+v",
					resolved,
				)
			}
			denied++
			continue
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return -1, denied, ctx.Err()
		case <-run.done:
			exitCode, waitErr := run.wait(ctx)
			return exitCode, denied, waitErr
		}
	}
}

func agentExitedBeforeApproval(
	ctx context.Context,
	run *agentRun,
) error {
	exitCode, waitErr := run.wait(ctx)
	return fmt.Errorf(
		"%s exited before a tool approval became pending: %w",
		run.clientLabel(),
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
	if config.clientID == acceptanceClientClaudeCode {
		deltaContext, cancelDelta := context.WithTimeout(
			ctx,
			2*time.Minute,
		)
		err = run.waitForDelta(deltaContext)
		cancelDelta()
		if err != nil {
			return err
		}
	} else {
		if err := waitForActiveProviderEgress(
			ctx,
			generation.control,
			run,
			2*time.Minute,
		); err != nil {
			return err
		}
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
