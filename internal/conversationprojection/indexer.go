// Package conversationprojection enriches the rebuildable Conversation index
// with exact identities recovered from Agent-client local authorities. It does
// not mutate Capture, Exchange, or semantic transcript authority.
package conversationprojection

import (
	"context"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/recording"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const maxExchangesPerRefresh = 10_000

type Options struct {
	IdentityInspection activity.IdentityInspectionReader
	RepairWriter       activity.ConversationRepairWriter
	Activities         activity.Reader
	Contents           exchangecontent.Reader
	CaptureRuns        capturerun.Reader
	Identities         activity.ConversationIdentityRepository
	Writer             activity.ConversationProjectionWriter
	Resolvers          map[string]agentconversation.ClientIdentityResolver
}

type Indexer struct {
	mu             sync.Mutex
	scopes         map[string]scanScope
	activities     activity.Reader
	contents       exchangecontent.Reader
	captureRuns    capturerun.Reader
	identities     activity.ConversationIdentityReader
	identityWriter activity.ConversationIdentityRepository
	repairWriter   activity.ConversationRepairWriter
	inspection     bool
	writer         activity.ConversationProjectionWriter
	resolvers      map[string]agentconversation.ClientIdentityResolver
}

type scanScope struct {
	view     recording.InspectionToken
	before   int64
	cursor   string
	repaired bool
}

func New(options Options) (*Indexer, error) {
	if options.Activities == nil || options.Contents == nil ||
		options.CaptureRuns == nil || (options.IdentityInspection == nil && (options.Identities == nil || options.Writer == nil)) {
		return nil, errors.New("Conversation projection dependencies are incomplete")
	}
	var identities activity.ConversationIdentityReader = options.Identities
	if options.IdentityInspection != nil {
		identities = options.IdentityInspection
		if _, ok := options.Activities.(recording.InspectionBoundary); !ok {
			return nil, errors.New("Activity inspection boundary is missing")
		}
		if _, ok := options.Contents.(recording.InspectionBoundary); !ok {
			return nil, errors.New("content inspection boundary is missing")
		}
	}
	resolvers := make(map[string]agentconversation.ClientIdentityResolver, len(options.Resolvers))
	for client, resolver := range options.Resolvers {
		if (client != "claude" && client != "codex") || resolver == nil {
			return nil, errors.New("Conversation projection resolver is invalid")
		}
		resolvers[client] = resolver
	}
	return &Indexer{
		scopes:     make(map[string]scanScope),
		activities: options.Activities, contents: options.Contents,
		captureRuns: options.CaptureRuns, identities: identities, identityWriter: options.Identities, repairWriter: options.RepairWriter, inspection: options.IdentityInspection != nil,
		writer: options.Writer, resolvers: resolvers,
	}, nil
}

// Reindex enriches bounded terminal Exchanges selected by a Conversation
// request. The first pass for each scope repairs identities whose projection
// was interrupted; later passes omit already completed local identities.
// Manual captures remain Exchange-scoped without AgentSession authority.
func (indexer *Indexer) Reindex(
	ctx context.Context,
	request activity.ConversationIndexRequest,
) error {
	if indexer == nil || ctx == nil || request.Validate() != nil {
		return activity.ErrInvalidEvent
	}
	if request.ManualCaptureID != "" || (indexer.inspection && indexer.repairWriter == nil) {
		return nil
	}
	indexer.mu.Lock()
	defer indexer.mu.Unlock()
	scope, knownScope := indexer.scopes[request.CaptureRunID]
	var expiry time.Time
	view, check, err := indexer.beginInspection(ctx)
	if err != nil {
		clear(indexer.scopes)
		return err
	}
	// A completed scan has no cursor left to reject. Bind its repaired flag
	// to the same full view as partial progress before selecting incremental mode.
	if indexer.inspection && scope.view != view {
		scope = scanScope{view: view}
	}
	next := scanScope{repaired: scope.repaired, view: scope.view}
	if err := indexer.reindex(ctx, request, scope, &next, &expiry, func() error { return check(expiry) }); err != nil {
		// A failed projection may have persisted its identity first. Force the
		// next pass to inspect those rows again instead of skipping them.
		clear(indexer.scopes)
		return err
	}
	if err := check(expiry); err != nil {
		clear(indexer.scopes)
		return err
	}
	if !knownScope && len(indexer.scopes) >= 256 {
		// ponytail: Bound per-scope repair state; an evicted scope safely
		// repeats its first full pass when it is visited again.
		clear(indexer.scopes)
	}
	scope.before, scope.cursor = next.before, next.cursor
	if next.before == 0 && next.cursor == "" {
		scope.repaired = true
	}
	indexer.scopes[request.CaptureRunID] = scope
	return nil
}

// CaptureRunCaughtUp reports whether the last successful Reindex for this
// Capture finished its repair budget. A partial pass must not idle-latch the
// control-plane scheduler, or the remaining cursor never advances.
func (indexer *Indexer) CaptureRunCaughtUp(captureRunID string) bool {
	if indexer == nil {
		return false
	}
	indexer.mu.Lock()
	defer indexer.mu.Unlock()
	scope, ok := indexer.scopes[captureRunID]
	return ok && scope.repaired && scope.before == 0 && scope.cursor == ""
}

func (indexer *Indexer) reindex(
	ctx context.Context,
	request activity.ConversationIndexRequest,
	scope scanScope,
	next *scanScope,
	expiry *time.Time,
	check func() error,
) error {
	records, continuation, err := indexer.terminals(ctx, request.CaptureRunID, scope)
	if err != nil {
		return err
	}
	*next = continuation
	type candidate struct {
		record                   activity.Record
		responseID               string
		protocolEvidence         []protocolcore.ProtocolEvidenceValue
		responseProtocolEvidence []protocolcore.ProtocolEvidenceValue
	}
	byRun := make(map[string][]candidate)
	for _, record := range records {
		if record.CaptureRunID == "" || (indexer.inspection && (record.InspectionRoot == nil || !record.InspectionRoot.Current)) {
			continue
		}
		var storedProtocolIdentity *agentconversation.ClientIdentity
		identity, identityErr := indexer.identities.GetConversationIdentity(ctx, record.SubjectID)
		switch {
		case identityErr == nil:
			if err := indexer.projectChecked(ctx, record, identity, check); err != nil {
				return err
			}
			if identity.Source == agentconversation.ClientIdentitySourceLocalState {
				continue
			}
			stored := identity.Clone()
			storedProtocolIdentity = &stored
		case !errors.Is(identityErr, activity.ErrExchangeNotFound):
			return identityErr
		}
		content, contentErr := indexer.contents.GetConversationEvidence(ctx, record.SubjectID)
		if errors.Is(contentErr, exchangecontent.ErrNotFound) {
			if storedProtocolIdentity != nil &&
				storedProtocolIdentity.ProviderResponseID != "" {
				byRun[record.CaptureRunID] = append(
					byRun[record.CaptureRunID],
					candidate{
						record:     record,
						responseID: storedProtocolIdentity.ProviderResponseID,
						protocolEvidence: protocolEvidenceFromIdentity(
							storedProtocolIdentity.ProtocolIDs,
						),
					},
				)
			}
			continue
		}
		if contentErr != nil {
			return contentErr
		}
		includeExpiry(expiry, content.ExpiresAt)
		responseID := content.ResponseID
		responseEvidence := content.ResponseProtocolEvidence
		if storedProtocolIdentity == nil {
			// Exact wire identifiers make the Conversation usable immediately,
			// before the Agent client flushes its append-only local session log.
			// Persist them outside transcript retention; PutConversationIdentity
			// later allows only a structurally consistent local-state deepening.
			if networkIdentity, found := agentconversation.ClientIdentityFromProtocolEvidence(
				content.RequestProtocolEvidence,
				responseID,
				record.OccurredAt,
			); found {
				if err := indexer.persist(ctx, record, networkIdentity, check); err != nil {
					return err
				}
			}
		}
		if responseID == "" && storedProtocolIdentity != nil {
			responseID = storedProtocolIdentity.ProviderResponseID
		}
		if responseID == "" {
			continue
		}
		protocolEvidence := append(
			[]protocolcore.ProtocolEvidenceValue(nil),
			content.RequestProtocolEvidence...,
		)
		if len(protocolEvidence) == 0 && storedProtocolIdentity != nil {
			protocolEvidence = protocolEvidenceFromIdentity(
				storedProtocolIdentity.ProtocolIDs,
			)
		}
		byRun[record.CaptureRunID] = append(byRun[record.CaptureRunID], candidate{
			record: record, responseID: responseID,
			protocolEvidence:         protocolEvidence,
			responseProtocolEvidence: responseEvidence,
		})
	}
	for captureRunID, candidates := range byRun {
		run, runErr := indexer.captureRuns.GetRun(ctx, captureRunID)
		if errors.Is(runErr, capturerun.ErrNotFound) {
			continue
		}
		if runErr != nil {
			return runErr
		}
		client := clientKind(run)
		resolver := indexer.resolvers[client]
		if resolver == nil {
			continue
		}
		counts := make(map[string]int, len(candidates))
		for _, item := range candidates {
			counts[item.responseID]++
		}
		lookups := make([]agentconversation.ClientIdentityLookup, 0, len(candidates))
		for _, item := range candidates {
			// One provider message cannot be evidence for two logical Exchanges.
			// Preserve both as unresolved instead of arbitrarily choosing one.
			if counts[item.responseID] != 1 {
				continue
			}
			lookups = append(lookups, agentconversation.ClientIdentityLookup{
				ProviderResponseID:       item.responseID,
				ObservedAt:               item.record.OccurredAt,
				ProtocolEvidence:         item.protocolEvidence,
				ResponseProtocolEvidence: item.responseProtocolEvidence,
			})
		}
		if len(lookups) == 0 {
			continue
		}
		resolved, resolveErr := resolver.ResolveBatch(ctx, run.CWD, lookups)
		if resolveErr != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("resolve %s Conversation identities: %w", client, resolveErr)
			}
			// Client-local logs are append-only evidence owned by another process.
			// A transient unreadable tail or unavailable client directory leaves
			// these Exchanges unresolved; it never hides the Activity authority.
			continue
		}
		for _, item := range candidates {
			if counts[item.responseID] != 1 {
				continue
			}
			identity, found := resolved[item.responseID]
			if !found {
				continue
			}
			err := indexer.persist(ctx, item.record, identity, check)
			if errors.Is(err, activity.ErrInvalidEvent) {
				// The client-local log names a different session or actor than
				// the wire already recorded for this Exchange (a Codex subagent
				// can do this). The wire identity stays authoritative; one
				// contradicting Exchange must not stall the rest of the index.
				continue
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func protocolEvidenceFromIdentity(
	values []agentconversation.ClientEvidenceValue,
) []protocolcore.ProtocolEvidenceValue {
	converted := make([]protocolcore.ProtocolEvidenceValue, 0, len(values))
	for _, value := range values {
		converted = append(converted, protocolcore.ProtocolEvidenceValue{
			Name: value.Name, Value: value.Value,
		})
	}
	return converted
}

// IdentityInspection retains the original metadata lifetime through outer
// enrichment. Persisted identity roots have no transcript expiry.
type IdentityInspection struct {
	Identity            agentconversation.ClientIdentity
	InspectionExpiresAt time.Time `json:"-"`
}

func (indexer *Indexer) Identity(ctx context.Context, exchangeID string) (agentconversation.ClientIdentity, error) {
	value, err := indexer.InspectIdentity(ctx, exchangeID)
	return value.Identity, err
}
func (indexer *Indexer) InspectIdentity(ctx context.Context, exchangeID string) (IdentityInspection, error) {
	if indexer == nil || ctx == nil {
		return IdentityInspection{}, activity.ErrInvalidEvent
	}
	_, check, err := indexer.beginInspection(ctx)
	if err != nil {
		return IdentityInspection{}, err
	}
	result, err := indexer.identity(ctx, exchangeID)
	if err != nil {
		return IdentityInspection{}, err
	}
	if err := check(result.InspectionExpiresAt); err != nil {
		return IdentityInspection{}, err
	}
	return result, nil
}
func (indexer *Indexer) identity(ctx context.Context, exchangeID string) (IdentityInspection, error) {
	identity, err := indexer.identities.GetConversationIdentity(ctx, exchangeID)
	if err == nil {
		return IdentityInspection{Identity: identity}, nil
	}
	if !errors.Is(err, activity.ErrExchangeNotFound) {
		return IdentityInspection{}, err
	}
	content, err := indexer.contents.GetConversationEvidence(ctx, exchangeID)
	if errors.Is(err, exchangecontent.ErrNotFound) {
		return IdentityInspection{}, activity.ErrExchangeNotFound
	}
	if err != nil {
		return IdentityInspection{}, err
	}
	identity, found := agentconversation.ClientIdentityFromProtocolEvidence(content.RequestProtocolEvidence, content.ResponseID, content.RecordedAt)
	if !found {
		return IdentityInspection{}, activity.ErrExchangeNotFound
	}
	return IdentityInspection{Identity: identity, InspectionExpiresAt: content.ExpiresAt}, nil
}

func (indexer *Indexer) terminals(ctx context.Context, captureRunID string, scope scanScope) ([]activity.Record, scanScope, error) {
	records := make([]activity.Record, 0, activity.MaxPageSize)
	consumed := 0
	for consumed < maxExchangesPerRefresh {
		// Keep the same limit across continuation requests: it is part of the
		// independent cursor's query binding, including an empty filtered page.
		page, err := indexer.activities.ListExchanges(ctx, activity.PageRequest{
			BeforeSequence: scope.before, Cursor: scope.cursor, Limit: activity.MaxPageSize,
			CaptureRunID: captureRunID, WithoutLocalConversationIdentity: scope.repaired,
		})
		if err != nil {
			return nil, scanScope{}, err
		}
		records = append(records, page.Items...)
		examined := len(page.Items)
		if indexer.inspection {
			examined = page.Consumed
			if examined < len(page.Items) || examined > activity.MaxPageSize || (page.NextCursor != "" && (examined == 0 || page.NextCursor == scope.cursor)) {
				return nil, scanScope{}, recording.ErrInspectionUnavailable
			}
			scope.cursor = page.NextCursor
		} else {
			scope.before = page.NextBeforeSequence
		}
		consumed += examined
		if scope.before == 0 && scope.cursor == "" {
			return records, scope, nil
		}
	}
	return records, scope, nil
}

func (indexer *Indexer) persist(ctx context.Context, record activity.Record, identity agentconversation.ClientIdentity, check func() error) error {
	if indexer.inspection {
		return indexer.projectChecked(ctx, record, identity, check)
	}
	if err := indexer.identityWriter.PutConversationIdentity(ctx, record.SubjectID, identity); err != nil {
		return err
	}
	return indexer.project(ctx, record, identity)
}
func (indexer *Indexer) projectChecked(ctx context.Context, record activity.Record, identity agentconversation.ClientIdentity, check func() error) error {
	if !indexer.inspection {
		return indexer.project(ctx, record, identity)
	}
	if record.InspectionRoot == nil || !record.InspectionRoot.Current || indexer.repairWriter == nil {
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	if err := indexer.activities.(recording.InspectionBoundary).Check(ctx, record.InspectionRoot.Token); err != nil {
		return err
	}
	return indexer.repairWriter.RepairConversation(ctx, activity.ConversationRepair{Root: *record.InspectionRoot, Identity: identity.Clone()})
}

func (indexer *Indexer) project(
	ctx context.Context,
	record activity.Record,
	identity agentconversation.ClientIdentity,
) error {
	ref, err := agentconversation.Project(agentconversation.ProjectionInput{
		CaptureRunID: record.CaptureRunID, ExchangeID: record.SubjectID,
		SourceDisplayName: record.SourceDisplayName, ClientIdentity: &identity,
	})
	if err != nil {
		return err
	}
	if record.Conversation != nil && *record.Conversation == ref {
		return nil
	}
	return indexer.writer.ReprojectConversation(ctx, record.SubjectID, ref)
}

func clientKind(run capturerun.View) string {
	if run.Adapter != nil {
		switch run.Adapter.ID {
		case "claude-code":
			return "claude"
		case "codex-cli":
			return "codex"
		}
	}
	label := strings.ToLower(strings.TrimSpace(run.ExecutableLabel))
	base := strings.ToLower(filepath.Base(run.CanonicalExecutablePath))
	if label == "claude" || strings.HasPrefix(base, "claude") {
		return "claude"
	}
	if label == "codex" || strings.HasPrefix(base, "codex") {
		return "codex"
	}
	return ""
}
