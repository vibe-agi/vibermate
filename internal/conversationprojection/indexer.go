// Package conversationprojection enriches the rebuildable Conversation index
// with exact identities recovered from Agent-client local authorities. It does
// not mutate Capture, Exchange, or semantic transcript authority.
package conversationprojection

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const maxExchangesPerRefresh = 10_000

type Options struct {
	Activities  activity.Reader
	Contents    exchangecontent.Reader
	CaptureRuns capturerun.Reader
	Identities  activity.ConversationIdentityRepository
	Writer      activity.ConversationProjectionWriter
	Resolvers   map[string]agentconversation.ClientIdentityResolver
}

type Indexer struct {
	activities  activity.Reader
	contents    exchangecontent.Reader
	captureRuns capturerun.Reader
	identities  activity.ConversationIdentityRepository
	writer      activity.ConversationProjectionWriter
	resolvers   map[string]agentconversation.ClientIdentityResolver
}

func New(options Options) (*Indexer, error) {
	if options.Activities == nil || options.Contents == nil ||
		options.CaptureRuns == nil || options.Identities == nil ||
		options.Writer == nil {
		return nil, errors.New("Conversation projection dependencies are incomplete")
	}
	resolvers := make(map[string]agentconversation.ClientIdentityResolver, len(options.Resolvers))
	for client, resolver := range options.Resolvers {
		if (client != "claude" && client != "codex") || resolver == nil {
			return nil, errors.New("Conversation projection resolver is invalid")
		}
		resolvers[client] = resolver
	}
	return &Indexer{
		activities: options.Activities, contents: options.Contents,
		captureRuns: options.CaptureRuns, identities: options.Identities,
		writer: options.Writer, resolvers: resolvers,
	}, nil
}

// Reindex enriches all bounded terminal Exchanges selected by a Conversation
// request before the caller reads the grouped index. Manual captures are kept
// Exchange-scoped because they do not yet have an AgentSession authority.
func (indexer *Indexer) Reindex(
	ctx context.Context,
	request activity.ConversationIndexRequest,
) error {
	if indexer == nil || ctx == nil || request.Validate() != nil {
		return activity.ErrInvalidEvent
	}
	if request.ManualCaptureID != "" {
		return nil
	}
	records, err := indexer.terminals(ctx, request.CaptureRunID)
	if err != nil {
		return err
	}
	type candidate struct {
		record                   activity.Record
		responseID               string
		protocolEvidence         []protocolcore.ProtocolEvidenceValue
		responseProtocolEvidence []protocolcore.ProtocolEvidenceValue
	}
	byRun := make(map[string][]candidate)
	for _, record := range records {
		if record.CaptureRunID == "" {
			continue
		}
		var storedProtocolIdentity *agentconversation.ClientIdentity
		identity, identityErr := indexer.identities.GetConversationIdentity(ctx, record.SubjectID)
		switch {
		case identityErr == nil:
			if err := indexer.project(ctx, record, identity); err != nil {
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
		content, contentErr := indexer.contents.Get(ctx, record.SubjectID)
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
		responseID := ""
		responseEvidence := []protocolcore.ProtocolEvidenceValue(nil)
		if content.Response != nil {
			responseID = content.Response.ID
			responseEvidence = append(
				responseEvidence,
				content.Response.ProtocolEvidence...,
			)
		}
		if storedProtocolIdentity == nil {
			// Exact wire identifiers make the Conversation usable immediately,
			// before the Agent client flushes its append-only local session log.
			// Persist them outside transcript retention; PutConversationIdentity
			// later allows only a structurally consistent local-state deepening.
			if networkIdentity, found := agentconversation.ClientIdentityFromProtocolEvidence(
				content.Request.ProtocolEvidence,
				responseID,
				record.OccurredAt,
			); found {
				if err := indexer.identities.PutConversationIdentity(
					ctx,
					record.SubjectID,
					networkIdentity,
				); err != nil {
					return err
				}
				if err := indexer.project(ctx, record, networkIdentity); err != nil {
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
			content.Request.ProtocolEvidence...,
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
			if err := indexer.identities.PutConversationIdentity(
				ctx,
				item.record.SubjectID,
				identity,
			); err != nil {
				return err
			}
			if err := indexer.project(ctx, item.record, identity); err != nil {
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

func (indexer *Indexer) Identity(
	ctx context.Context,
	exchangeID string,
) (agentconversation.ClientIdentity, error) {
	if indexer == nil || ctx == nil {
		return agentconversation.ClientIdentity{}, activity.ErrInvalidEvent
	}
	identity, err := indexer.identities.GetConversationIdentity(ctx, exchangeID)
	if err == nil || !errors.Is(err, activity.ErrExchangeNotFound) {
		return identity, err
	}
	content, contentErr := indexer.contents.Get(ctx, exchangeID)
	if errors.Is(contentErr, exchangecontent.ErrNotFound) {
		return agentconversation.ClientIdentity{}, activity.ErrExchangeNotFound
	}
	if contentErr != nil {
		return agentconversation.ClientIdentity{}, contentErr
	}
	responseID := ""
	if content.Response != nil {
		responseID = content.Response.ID
	}
	identity, found := agentconversation.ClientIdentityFromProtocolEvidence(
		content.Request.ProtocolEvidence,
		responseID,
		content.RecordedAt,
	)
	if !found {
		return agentconversation.ClientIdentity{}, activity.ErrExchangeNotFound
	}
	return identity, nil
}

func (indexer *Indexer) terminals(
	ctx context.Context,
	captureRunID string,
) ([]activity.Record, error) {
	records := make([]activity.Record, 0, activity.MaxPageSize)
	var before int64
	for len(records) < maxExchangesPerRefresh {
		limit := activity.MaxPageSize
		if remaining := maxExchangesPerRefresh - len(records); remaining < limit {
			limit = remaining
		}
		page, err := indexer.activities.ListExchanges(ctx, activity.PageRequest{
			BeforeSequence: before,
			Limit:          limit,
			CaptureRunID:   captureRunID,
		})
		if err != nil {
			return nil, err
		}
		records = append(records, page.Items...)
		if page.NextBeforeSequence == 0 {
			break
		}
		before = page.NextBeforeSequence
	}
	return records, nil
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
