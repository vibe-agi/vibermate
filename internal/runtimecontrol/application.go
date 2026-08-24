// Package runtimecontrol composes the host-neutral management API over one
// ProductRuntime. Desktop and network Server Hosts own different transports
// and authentication, but they expose the same Environment, Endpoint,
// account, Capture, and evidence semantics after authentication terminates.
package runtimecontrol

import (
	"errors"

	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/conversationprojection"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/modelcatalog"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

type Options struct {
	Runtime                *productruntime.Runtime
	Readiness              desktopcontrol.ReadinessReader
	Clock                  desktopcontrol.Clock
	ResolveLocalIdentities bool
}

func New(options Options) (*desktopcontrol.Handler, error) {
	if options.Runtime == nil || options.Readiness == nil || options.Clock == nil {
		return nil, errors.New("Runtime management application dependencies are incomplete")
	}
	identityResolvers := map[string]agentconversation.ClientIdentityResolver{}
	if options.ResolveLocalIdentities {
		if root, err := agentconversation.DefaultClaudeProjectsRoot(); err == nil {
			if resolver, resolverErr := agentconversation.NewClaudeIdentityResolver(root); resolverErr == nil {
				identityResolvers["claude"] = resolver
			}
		}
		if root, err := agentconversation.DefaultCodexSessionsRoot(); err == nil {
			if resolver, resolverErr := agentconversation.NewCodexIdentityResolver(root); resolverErr == nil {
				identityResolvers["codex"] = resolver
			}
		}
	}
	indexer, err := conversationprojection.New(conversationprojection.Options{
		Activities: options.Runtime.Activities(), Contents: options.Runtime.ExchangeContents(),
		CaptureRuns: options.Runtime.CaptureRunReader(), Identities: options.Runtime.ConversationIdentities(),
		Writer: options.Runtime.ConversationProjectionWriter(), Resolvers: identityResolvers,
	})
	if err != nil {
		return nil, err
	}
	metadata, err := modelcatalog.NewModelsDev(modelcatalog.ModelsDevOptions{
		Transport: options.Runtime, Clock: options.Clock,
	})
	if err != nil {
		return nil, err
	}
	models, err := modelcatalog.New(modelcatalog.Options{
		Endpoints: options.Runtime.UpstreamEndpoints(), Credentials: options.Runtime.ProviderAccounts(),
		Transport: options.Runtime, Clock: options.Clock,
	})
	if err != nil {
		return nil, err
	}
	return desktopcontrol.New(desktopcontrol.Options{
		Readiness: options.Readiness, Status: options.Runtime,
		Environments: options.Runtime.Environments(), Assignments: options.Runtime.CaptureAssignments(),
		Activities: options.Runtime.Activities(), ConversationIndexer: indexer,
		Contents: options.Runtime.ExchangeContents(), Connections: options.Runtime.ConnectionEvents(),
		Egress: options.Runtime.EgressAttempts(), Approvals: options.Runtime.ToolApprovals(),
		Endpoints: options.Runtime.UpstreamEndpoints(), Models: models, ClientModels: metadata,
		Accounts: options.Runtime.ProviderAccounts(), RawEvidence: options.Runtime.RawEvidence(),
		Offline: options.Runtime, ConnectionRules: options.Runtime.ConnectionRules(),
		CaptureRuns: options.Runtime.CaptureRunReader(), ManualCaptures: options.Runtime.ManualCaptures(),
		Archive: options.Runtime.EvidenceArchive(), ArchiveBarrier: options.Runtime.EvidenceArchiveBarrier(),
		Clock: options.Clock,
	})
}
