package environment

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

type CaptureReference struct {
	Capture         captureidentity.Reference
	Program         string
	MachineLabel    string
	WorkspaceLabel  string
	LaunchAuthority LaunchAuthorityBoundary
	Bindings        []ConnectionBinding
}

type CaptureInspector interface {
	AffectedCaptures(context.Context, EnvironmentID, int) ([]CaptureReference, error)
}

// CaptureTransitionCoordinator owns the publish fence between immutable
// Environment revisions. It must revalidate the preview against live Capture
// assignments, stop admission only where required, drain and close incompatible
// downstream connections, and hold that fence until Release.
type CaptureTransitionCoordinator interface {
	PrepareEnvironmentTransition(context.Context, EnvironmentTransition) (EnvironmentTransitionLease, error)
}

type EnvironmentTransition struct {
	ActiveExists bool
	Active       EnvironmentSnapshot
	Candidate    EnvironmentSnapshot
	Expected     ImpactPreview
}

type EnvironmentTransitionLease interface {
	Commit()
	Release()
}

type CompatibilityClassification string

const (
	CompatibilityHotSwitch         CompatibilityClassification = "hot_switch"
	CompatibilityReconnectRequired CompatibilityClassification = "reconnect_required"
	CompatibilityRestartRequired   CompatibilityClassification = "restart_required"
)

type ImpactReference struct {
	Capture        CaptureReference
	Classification CompatibilityClassification
}

type ImpactPreview struct {
	EnvironmentID          EnvironmentID
	BaseRevision           Revision
	DraftRevision          Revision
	CandidateDigest        CandidateDigest
	Classification         CompatibilityClassification
	HotSwitchCount         int
	ReconnectRequiredCount int
	RestartRequiredCount   int
	Affected               []ImpactReference
}

func (preview ImpactPreview) Clone() ImpactPreview {
	preview.Affected = slices.Clone(preview.Affected)
	for index := range preview.Affected {
		preview.Affected[index].Capture.Bindings = slices.Clone(preview.Affected[index].Capture.Bindings)
	}
	return preview
}

type DraftCommand struct {
	ExpectedBaseRevision Revision
	// ExpectedDraftRevision is the current private draft revision, or zero
	// when GetDraft reports that no private draft exists. Published drafts are
	// consumed; their historical allocation number is not a caller CAS token.
	ExpectedDraftRevision Revision
	Candidate             Environment
}

type Publisher interface {
	SaveDraft(context.Context, DraftCommand) (Draft, error)
	Preview(context.Context, EnvironmentID, Revision) (ImpactPreview, error)
	Publish(context.Context, ImpactPreview) (CommitResult, error)
}

// Reader exposes current, draft, and immutable historical Environment state to
// the control plane. Data-plane callers must continue to use SnapshotResolver,
// which rejects disabled Environments and never reads drafts.
type Reader interface {
	List(context.Context) ([]EnvironmentSnapshot, error)
	Get(context.Context, EnvironmentID) (EnvironmentSnapshot, error)
	GetDraft(context.Context, EnvironmentID) (Draft, error)
	GetRevision(context.Context, EnvironmentID, Revision) (EnvironmentSnapshot, error)
}

// AccountReference is one current, published Route reference to a managed
// ProviderAccount. It contains no credential material and is safe to return
// to the control plane when an account cannot yet be removed.
type AccountReference struct {
	EnvironmentID       EnvironmentID
	EnvironmentName     string
	EnvironmentRevision Revision
	RouteID             UpstreamRouteID
	RouteRevision       Revision
}

// AccountDeletionGuard serializes account retirement with Environment draft
// writes and publication. A caller may delete only inside the callback and
// only when the returned reference set is empty.
type AccountDeletionGuard interface {
	GuardAccountDeletion(
		context.Context,
		string,
		func() error,
	) ([]AccountReference, error)
}

// Controller is the single Environment control-plane authority.
// Deleter retires an Environment once nothing holds it.
type Deleter interface {
	Delete(context.Context, EnvironmentID, ...HolderLookup) (resourcedeletion.Result, error)
	HoldersForEndpoint(context.Context, string) ([]resourcedeletion.Holder, error)
}

type Controller interface {
	Publisher
	Reader
	SnapshotResolver
	Deleter
	Health() ProjectionHealth
}

type Manager struct {
	repository  Repository
	compiler    Compiler
	projection  SnapshotProjection
	inspector   CaptureInspector
	transitions CaptureTransitionCoordinator
	writes      sync.Mutex
}

var (
	_ Controller           = (*Manager)(nil)
	_ SnapshotResolver     = (*Manager)(nil)
	_ AccountDeletionGuard = (*Manager)(nil)
)

func NewManager(ctx context.Context, repository Repository, compiler Compiler, projection SnapshotProjection, inspector CaptureInspector) (*Manager, error) {
	if ctx == nil || repository == nil || projection == nil {
		return nil, errors.New("Environment manager dependencies are incomplete")
	}
	aggregates, err := repository.LoadAllActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("recover Environment aggregates: %w", err)
	}
	snapshots := make([]EnvironmentSnapshot, 0, len(aggregates))
	for _, aggregate := range aggregates {
		snapshot, compileErr := compiler.Compile(aggregate)
		if compileErr != nil {
			return nil, fmt.Errorf("%w: recover environmentId=%q: %w", ErrInvalidRepositoryState, aggregate.ID, compileErr)
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := projection.Restore(snapshots); err != nil {
		return nil, fmt.Errorf("restore Environment projection: %w", err)
	}
	var transitions CaptureTransitionCoordinator
	if inspector != nil {
		var ok bool
		transitions, ok = inspector.(CaptureTransitionCoordinator)
		if !ok {
			return nil, errors.New("Environment Capture inspector cannot coordinate revision transitions")
		}
	}
	return &Manager{
		repository: repository, compiler: compiler, projection: projection,
		inspector: inspector, transitions: transitions,
	}, nil
}

func (manager *Manager) Resolve(id EnvironmentID) (EnvironmentSnapshot, error) {
	return manager.projection.Resolve(id)
}

func (manager *Manager) ResolveClientOrigin(id EnvironmentID, origin originidentity.ClientOrigin) (ClientEndpointSnapshot, error) {
	return manager.projection.ResolveClientOrigin(id, origin)
}

func (manager *Manager) Health() ProjectionHealth { return manager.projection.Health() }

func (manager *Manager) GuardAccountDeletion(
	ctx context.Context,
	accountID string,
	deleteAccount func() error,
) ([]AccountReference, error) {
	if ctx == nil || validateID("ProviderAccount ID", accountID) != nil || deleteAccount == nil {
		return nil, ErrInvalidEnvironment
	}
	manager.writes.Lock()
	defer manager.writes.Unlock()
	aggregates, err := manager.repository.LoadAllActive(ctx)
	if err != nil {
		return nil, err
	}
	references := make([]AccountReference, 0)
	for _, aggregate := range aggregates {
		for _, endpoint := range aggregate.ClientEndpoints {
			for _, plan := range endpoint.ProtocolPlans {
				for _, route := range plan.UpstreamPlan.Routes {
					if route.AccountPolicy.Mode != AccountModeManaged ||
						!slices.Contains(route.AccountPolicy.CandidateAccountIDs, accountID) {
						continue
					}
					references = append(references, AccountReference{
						EnvironmentID: aggregate.ID, EnvironmentName: aggregate.Name,
						EnvironmentRevision: aggregate.Revision,
						RouteID:             route.ID, RouteRevision: route.Revision,
					})
				}
			}
		}
	}
	sort.Slice(references, func(left, right int) bool {
		if references[left].EnvironmentID != references[right].EnvironmentID {
			return references[left].EnvironmentID < references[right].EnvironmentID
		}
		return references[left].RouteID < references[right].RouteID
	})
	if len(references) != 0 {
		return references, nil
	}
	if err := deleteAccount(); err != nil {
		return nil, err
	}
	return []AccountReference{}, nil
}

// HolderLookup reports what would break if an Environment went away.
//
// The Manager owns one holder itself — a Capture that is still running, which
// it can see through the inspector. Everything else that can hold an
// Environment lives outside this package, so it arrives as a lookup rather than
// as a dependency this package would otherwise have to know about.
type HolderLookup func(
	context.Context,
	EnvironmentID,
) ([]resourcedeletion.Holder, error)

// Delete retires an Environment once nothing depends on it.
//
// A running Capture would lose the authority its admission decisions are frozen
// against mid-flight. Callers add the holders they know about — a workspace
// default is the other one today. Holders are reported, never resolved:
// choosing which Capture to stop or which workspace to repoint is the user's
// decision, not this manager's.
//
// Historical evidence is deliberately not a holder. Frozen Exchanges resolve
// immutable revisions and Retire leaves those in place, so an Environment can
// leave the product without leaving the record.
func (manager *Manager) Delete(
	ctx context.Context,
	id EnvironmentID,
	extra ...HolderLookup,
) (resourcedeletion.Result, error) {
	if ctx == nil || validateID("Environment ID", id.String()) != nil {
		return resourcedeletion.Result{}, ErrInvalidEnvironment
	}
	manager.writes.Lock()
	defer manager.writes.Unlock()
	if manager.inspector == nil {
		// Without an inspector the running-Capture holder cannot be consulted,
		// and an unchecked delete is the one outcome this method prevents.
		return resourcedeletion.Result{}, ErrInvalidEnvironment
	}
	captures, err := manager.inspector.AffectedCaptures(ctx, id, MaxCaptureImpact+1)
	if err != nil {
		return resourcedeletion.Result{}, err
	}
	holders := make([]resourcedeletion.Holder, 0, len(captures))
	for _, capture := range captures {
		holders = append(holders, resourcedeletion.Holder{
			Kind:   resourcedeletion.KindRunningCapture,
			ID:     capture.Capture.Key(),
			Label:  captureHolderLabel(capture),
			Detail: capture.MachineLabel,
		})
	}
	for _, lookup := range extra {
		if lookup == nil {
			return resourcedeletion.Result{}, ErrInvalidEnvironment
		}
		found, lookupErr := lookup(ctx, id)
		if lookupErr != nil {
			return resourcedeletion.Result{}, lookupErr
		}
		holders = append(holders, found...)
	}
	if len(holders) != 0 {
		return resourcedeletion.Refused(holders)
	}
	retired, err := manager.repository.Retire(ctx, id)
	if err != nil {
		return resourcedeletion.Result{}, err
	}
	if !retired {
		return resourcedeletion.Result{}, ErrEnvironmentNotFound
	}
	if err := manager.projection.Retire(id); err != nil {
		manager.projection.MarkUnavailable(id)
		return resourcedeletion.Result{}, errors.Join(ErrProjectionUnavailable, err)
	}
	return resourcedeletion.Completed(), nil
}

// captureHolderLabel names a Capture the way its own directory does, so the
// refusal points at a row the user can actually find.
func captureHolderLabel(capture CaptureReference) string {
	program := strings.TrimSpace(capture.Program)
	workspace := strings.TrimSpace(capture.WorkspaceLabel)
	switch {
	case program != "" && workspace != "":
		return program + " · " + workspace
	case program != "":
		return program
	case workspace != "":
		return workspace
	default:
		return capture.Capture.Key()
	}
}

func (manager *Manager) List(ctx context.Context) ([]EnvironmentSnapshot, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is nil", ErrInvalidEnvironment)
	}
	aggregates, err := manager.repository.LoadAllActive(ctx)
	if err != nil {
		return nil, err
	}
	snapshots := make([]EnvironmentSnapshot, 0, len(aggregates)+1)
	snapshots = append(snapshots, SystemTransparentSnapshot())
	for _, aggregate := range aggregates {
		snapshot, compileErr := manager.compiler.Compile(aggregate)
		if compileErr != nil {
			return nil, fmt.Errorf("%w: read environmentId=%q: %w", ErrInvalidRepositoryState, aggregate.ID, compileErr)
		}
		snapshots = append(snapshots, snapshot)
	}
	sort.Slice(snapshots, func(left, right int) bool {
		if snapshots[left].SystemOwned() != snapshots[right].SystemOwned() {
			return snapshots[left].SystemOwned()
		}
		return snapshots[left].ID() < snapshots[right].ID()
	})
	return snapshots, nil
}

func (manager *Manager) Get(ctx context.Context, id EnvironmentID) (EnvironmentSnapshot, error) {
	if ctx == nil {
		return EnvironmentSnapshot{}, fmt.Errorf("%w: context is nil", ErrInvalidEnvironment)
	}
	if id == SystemTransparentID {
		return SystemTransparentSnapshot(), nil
	}
	aggregate, exists, err := manager.repository.LoadActive(ctx, id)
	if err != nil {
		return EnvironmentSnapshot{}, err
	}
	if !exists {
		return EnvironmentSnapshot{}, fmt.Errorf("%w: environmentId=%q", ErrEnvironmentNotFound, id)
	}
	return manager.compiler.Compile(aggregate)
}

func (manager *Manager) GetDraft(ctx context.Context, id EnvironmentID) (Draft, error) {
	if ctx == nil || id == SystemTransparentID {
		return Draft{}, ErrInvalidEnvironment
	}
	draft, exists, err := manager.repository.LoadDraft(ctx, id)
	if err != nil {
		return Draft{}, err
	}
	if !exists {
		return Draft{}, fmt.Errorf("%w: environmentId=%q", ErrDraftNotFound, id)
	}
	return draft.Clone(), nil
}

func (manager *Manager) GetRevision(ctx context.Context, id EnvironmentID, revision Revision) (EnvironmentSnapshot, error) {
	if ctx == nil || revision == 0 {
		return EnvironmentSnapshot{}, ErrInvalidEnvironment
	}
	if id == SystemTransparentID {
		snapshot := SystemTransparentSnapshot()
		if revision != snapshot.Revision() {
			return EnvironmentSnapshot{}, fmt.Errorf("%w: environmentId=%q revision=%d", ErrEnvironmentNotFound, id, revision)
		}
		return snapshot, nil
	}
	aggregate, exists, err := manager.repository.LoadRevision(ctx, id, revision)
	if err != nil {
		return EnvironmentSnapshot{}, err
	}
	if !exists {
		return EnvironmentSnapshot{}, fmt.Errorf("%w: environmentId=%q revision=%d", ErrEnvironmentNotFound, id, revision)
	}
	return manager.compiler.Compile(aggregate)
}

func (manager *Manager) SaveDraft(ctx context.Context, command DraftCommand) (Draft, error) {
	if ctx == nil {
		return Draft{}, fmt.Errorf("%w: context is nil", ErrInvalidEnvironment)
	}
	candidate := command.Candidate.Clone()
	if candidate.ID == SystemTransparentID {
		return Draft{}, ErrSystemEnvironment
	}
	if candidate.Revision != command.ExpectedBaseRevision+1 || command.ExpectedBaseRevision >= MaxRevision {
		return Draft{}, fmt.Errorf("%w: candidate root revision must advance its base exactly once", ErrInvalidTransition)
	}
	manager.writes.Lock()
	defer manager.writes.Unlock()
	active, exists, err := manager.repository.LoadActive(ctx, candidate.ID)
	if err != nil {
		return Draft{}, err
	}
	if (exists && active.Revision != command.ExpectedBaseRevision) || (!exists && command.ExpectedBaseRevision != 0) {
		return Draft{}, ErrRevisionConflict
	}
	if exists {
		candidate, err = materializeIdentityHistory(&active, candidate)
		if err != nil {
			return Draft{}, err
		}
		if err := ValidateTransition(active, candidate); err != nil {
			return Draft{}, err
		}
	} else {
		candidate, err = materializeIdentityHistory(nil, candidate)
		if err != nil {
			return Draft{}, err
		}
		if err := validateNewChildren(candidate); err != nil {
			return Draft{}, err
		}
	}
	snapshot, err := manager.compiler.Compile(candidate)
	if err != nil {
		return Draft{}, err
	}
	draft, err := manager.repository.SaveDraft(ctx, DraftMutation{
		EnvironmentID: candidate.ID, ExpectedBaseRevision: command.ExpectedBaseRevision,
		ExpectedDraftRevision: command.ExpectedDraftRevision, Candidate: candidate,
		CandidateDigest: snapshot.Digest(),
	})
	if err != nil {
		return Draft{}, fmt.Errorf("save Environment draft: %w", err)
	}
	return draft.Clone(), nil
}

func validateNewChildren(candidate Environment) error {
	for _, endpoint := range candidate.ClientEndpoints {
		if endpoint.Revision != 1 {
			return newChildRevision("ClientEndpoint", endpoint.ID.String())
		}
		for _, plan := range endpoint.ProtocolPlans {
			if plan.Revision != 1 {
				return newChildRevision("ClientProtocolPlan", plan.ID.String())
			}
			for _, route := range plan.UpstreamPlan.Routes {
				if route.Revision != 1 {
					return newChildRevision("UpstreamRoute", route.ID.String())
				}
			}
		}
	}
	return nil
}

func (manager *Manager) Preview(ctx context.Context, id EnvironmentID, draftRevision Revision) (ImpactPreview, error) {
	if ctx == nil || id == SystemTransparentID || draftRevision == 0 {
		return ImpactPreview{}, ErrInvalidEnvironment
	}
	draft, exists, err := manager.repository.LoadDraft(ctx, id)
	if err != nil {
		return ImpactPreview{}, err
	}
	if !exists {
		return ImpactPreview{}, ErrDraftNotFound
	}
	if draft.Revision != draftRevision {
		return ImpactPreview{}, ErrRevisionConflict
	}
	candidate, err := manager.compiler.Compile(draft.Candidate)
	if err != nil || candidate.Digest() != draft.CandidateDigest {
		return ImpactPreview{}, fmt.Errorf("%w: draft candidate changed", ErrInvalidRepositoryState)
	}
	var active EnvironmentSnapshot
	activeAggregate, activeExists, err := manager.repository.LoadActive(ctx, id)
	if err != nil {
		return ImpactPreview{}, err
	}
	if (activeExists && activeAggregate.Revision != draft.BaseRevision) || (!activeExists && draft.BaseRevision != 0) {
		return ImpactPreview{}, ErrRevisionConflict
	}
	if activeExists {
		active, err = manager.compiler.Compile(activeAggregate)
		if err != nil {
			return ImpactPreview{}, err
		}
	}
	refs := []CaptureReference(nil)
	if manager.inspector != nil {
		refs, err = manager.inspector.AffectedCaptures(ctx, id, MaxCaptureImpact+1)
		if err != nil {
			return ImpactPreview{}, fmt.Errorf("inspect affected Captures: %w", err)
		}
		if len(refs) > MaxCaptureImpact {
			return ImpactPreview{}, ErrImpactLimitExceeded
		}
		sort.Slice(refs, func(left, right int) bool {
			if refs[left].Capture.Kind != refs[right].Capture.Kind {
				return refs[left].Capture.Kind < refs[right].Capture.Kind
			}
			return refs[left].Capture.ID < refs[right].Capture.ID
		})
		for index, ref := range refs {
			if ref.Capture.Validate() != nil || ref.LaunchAuthority.Validate() != nil {
				return ImpactPreview{}, fmt.Errorf("%w: affected Capture reference is invalid", ErrInvalidEnvironment)
			}
			refs[index].Bindings = slices.Clone(ref.Bindings)
			sortConnectionBindings(refs[index].Bindings)
			for bindingIndex, binding := range refs[index].Bindings {
				if validateConnectionBindingShape(binding) != nil {
					return ImpactPreview{}, fmt.Errorf("%w: affected Capture binding is invalid", ErrInvalidEnvironment)
				}
				if bindingIndex > 0 && binding == refs[index].Bindings[bindingIndex-1] {
					return ImpactPreview{}, fmt.Errorf("%w: affected Capture binding is duplicated", ErrInvalidEnvironment)
				}
			}
			if index > 0 && ref.Capture == refs[index-1].Capture {
				return ImpactPreview{}, fmt.Errorf("%w: affected Capture reference is duplicated", ErrInvalidEnvironment)
			}
		}
	}
	preview := ImpactPreview{
		EnvironmentID: id, BaseRevision: draft.BaseRevision, DraftRevision: draft.Revision,
		CandidateDigest: candidate.Digest(), Classification: CompatibilityHotSwitch,
		Affected: make([]ImpactReference, 0, len(refs)),
	}
	for _, ref := range refs {
		classification := classifyCapture(active, activeExists, candidate, ref)
		switch classification {
		case CompatibilityRestartRequired:
			preview.Classification = CompatibilityRestartRequired
			preview.RestartRequiredCount++
		case CompatibilityReconnectRequired:
			if preview.Classification != CompatibilityRestartRequired {
				preview.Classification = CompatibilityReconnectRequired
			}
			preview.ReconnectRequiredCount++
		default:
			preview.HotSwitchCount++
		}
		preview.Affected = append(preview.Affected, ImpactReference{Capture: ref, Classification: classification})
	}
	return preview.Clone(), nil
}

func classifyCapture(active EnvironmentSnapshot, activeExists bool, candidate EnvironmentSnapshot, ref CaptureReference) CompatibilityClassification {
	if err := ref.LaunchAuthority.Covers(candidate); err != nil {
		return CompatibilityRestartRequired
	}
	if !activeExists || active.State() != StateActive || candidate.State() != StateActive {
		return CompatibilityReconnectRequired
	}
	for _, binding := range ref.Bindings {
		classification, err := ClassifyConnectionTransition(active, candidate, binding)
		if err != nil || classification == CompatibilityReconnectRequired {
			return CompatibilityReconnectRequired
		}
	}
	return CompatibilityHotSwitch
}

func sortConnectionBindings(bindings []ConnectionBinding) {
	sort.Slice(bindings, func(left, right int) bool {
		if bindings[left].Mode != bindings[right].Mode {
			return bindings[left].Mode < bindings[right].Mode
		}
		if bindings[left].ClientOrigin != bindings[right].ClientOrigin {
			return bindings[left].ClientOrigin.String() < bindings[right].ClientOrigin.String()
		}
		if bindings[left].ClientEndpointID != bindings[right].ClientEndpointID {
			return bindings[left].ClientEndpointID < bindings[right].ClientEndpointID
		}
		return bindings[left].CompatibilityDigest.String() < bindings[right].CompatibilityDigest.String()
	})
}

func (manager *Manager) Publish(ctx context.Context, preview ImpactPreview) (CommitResult, error) {
	if ctx == nil || preview.EnvironmentID == SystemTransparentID || preview.DraftRevision == 0 || preview.CandidateDigest == (CandidateDigest{}) {
		return CommitResult{Outcome: CommitOutcomeNotCommitted}, ErrPreviewStale
	}
	manager.writes.Lock()
	defer manager.writes.Unlock()
	draft, exists, err := manager.repository.LoadDraft(ctx, preview.EnvironmentID)
	if err != nil || !exists {
		if err == nil {
			err = ErrDraftNotFound
		}
		return CommitResult{Outcome: CommitOutcomeNotCommitted}, errors.Join(ErrPreviewStale, err)
	}
	if draft.EnvironmentID != preview.EnvironmentID || draft.BaseRevision != preview.BaseRevision ||
		draft.Revision != preview.DraftRevision || draft.CandidateDigest != preview.CandidateDigest {
		return CommitResult{Outcome: CommitOutcomeNotCommitted}, ErrPreviewStale
	}
	active, activeExists, err := manager.repository.LoadActive(ctx, preview.EnvironmentID)
	if err != nil {
		return CommitResult{Outcome: CommitOutcomeNotCommitted}, err
	}
	if (activeExists && active.Revision != preview.BaseRevision) || (!activeExists && preview.BaseRevision != 0) {
		return CommitResult{Outcome: CommitOutcomeConflict, ActualRevision: active.Revision}, ErrRevisionConflict
	}
	if activeExists {
		if err := ValidateTransition(active, draft.Candidate); err != nil {
			return CommitResult{Outcome: CommitOutcomeNotCommitted}, err
		}
	} else if err := validateNewChildren(draft.Candidate); err != nil {
		return CommitResult{Outcome: CommitOutcomeNotCommitted}, err
	}
	snapshot, err := manager.compiler.Compile(draft.Candidate)
	if err != nil || snapshot.Digest() != preview.CandidateDigest {
		return CommitResult{Outcome: CommitOutcomeNotCommitted}, errors.Join(ErrPreviewStale, err)
	}
	var activeSnapshot EnvironmentSnapshot
	if activeExists {
		activeSnapshot, err = manager.compiler.Compile(active)
		if err != nil {
			return CommitResult{Outcome: CommitOutcomeNotCommitted}, err
		}
	}
	freshPreview, err := manager.Preview(ctx, preview.EnvironmentID, preview.DraftRevision)
	if err != nil || !sameImpactPreview(preview, freshPreview) {
		return CommitResult{Outcome: CommitOutcomeNotCommitted}, errors.Join(ErrPreviewStale, err)
	}
	if freshPreview.RestartRequiredCount != 0 ||
		freshPreview.Classification == CompatibilityRestartRequired {
		return CommitResult{Outcome: CommitOutcomeNotCommitted},
			ErrLaunchAuthorityRestartRequired
	}
	var transitionLease EnvironmentTransitionLease
	if manager.transitions != nil {
		transitionLease, err = manager.transitions.PrepareEnvironmentTransition(ctx, EnvironmentTransition{
			ActiveExists: activeExists,
			Active:       activeSnapshot,
			Candidate:    snapshot,
			Expected:     freshPreview,
		})
		if err != nil || transitionLease == nil {
			return CommitResult{Outcome: CommitOutcomeNotCommitted}, errors.Join(ErrTransitionUnavailable, err)
		}
		defer transitionLease.Release()
	} else if len(freshPreview.Affected) != 0 {
		return CommitResult{Outcome: CommitOutcomeNotCommitted}, ErrTransitionUnavailable
	}
	result, commitErr := manager.repository.PublishDraft(ctx, PublishMutation{
		EnvironmentID: preview.EnvironmentID, ExpectedBaseRevision: preview.BaseRevision,
		DraftRevision: preview.DraftRevision, CandidateDigest: preview.CandidateDigest,
		Candidate: draft.Candidate,
	})
	switch result.Outcome {
	case CommitOutcomeCommitted:
		committedDigest, digestErr := Digest(result.Aggregate)
		if commitErr != nil || digestErr != nil || result.ActualRevision != snapshot.Revision() ||
			result.Aggregate.ID != snapshot.ID() || committedDigest != snapshot.Digest() {
			manager.projection.MarkUnavailable(preview.EnvironmentID)
			return result, errors.Join(ErrCommitOutcomeUnknown, commitErr, digestErr)
		}
		if err := manager.projection.Publish(snapshot); err != nil {
			manager.projection.MarkUnavailable(preview.EnvironmentID)
			return result, errors.Join(ErrProjectionUnavailable, err)
		}
		if transitionLease != nil {
			transitionLease.Commit()
		}
		return result, nil
	case CommitOutcomeConflict:
		return result, errors.Join(ErrRevisionConflict, commitErr)
	case CommitOutcomeIndeterminate:
		manager.projection.MarkUnavailable(preview.EnvironmentID)
		return result, errors.Join(ErrCommitOutcomeUnknown, commitErr)
	default:
		return result, errors.Join(ErrWriteNotCommitted, commitErr)
	}
}

func sameImpactPreview(left, right ImpactPreview) bool {
	if left.EnvironmentID != right.EnvironmentID || left.BaseRevision != right.BaseRevision ||
		left.DraftRevision != right.DraftRevision || left.CandidateDigest != right.CandidateDigest ||
		left.Classification != right.Classification || left.HotSwitchCount != right.HotSwitchCount ||
		left.ReconnectRequiredCount != right.ReconnectRequiredCount ||
		left.RestartRequiredCount != right.RestartRequiredCount || len(left.Affected) != len(right.Affected) {
		return false
	}
	for index := range left.Affected {
		leftImpact, rightImpact := left.Affected[index], right.Affected[index]
		if leftImpact.Classification != rightImpact.Classification ||
			leftImpact.Capture.Capture != rightImpact.Capture.Capture ||
			leftImpact.Capture.LaunchAuthority != rightImpact.Capture.LaunchAuthority ||
			leftImpact.Capture.Program != rightImpact.Capture.Program ||
			leftImpact.Capture.MachineLabel != rightImpact.Capture.MachineLabel ||
			leftImpact.Capture.WorkspaceLabel != rightImpact.Capture.WorkspaceLabel ||
			!slices.Equal(leftImpact.Capture.Bindings, rightImpact.Capture.Bindings) {
			return false
		}
	}
	return true
}

// HoldersForEndpoint reports the published routes that name an upstream
// Endpoint.
//
// It reads the same aggregates GuardAccountDeletion does, for the same reason:
// this package holds the only complete picture of what a published Environment
// points at, so the question has to be answered here even though the resource
// being deleted lives elsewhere.
func (manager *Manager) HoldersForEndpoint(
	ctx context.Context,
	endpointID string,
) ([]resourcedeletion.Holder, error) {
	if ctx == nil || strings.TrimSpace(endpointID) == "" {
		return nil, ErrInvalidEnvironment
	}
	aggregates, err := manager.repository.LoadAllActive(ctx)
	if err != nil {
		return nil, err
	}
	holders := make([]resourcedeletion.Holder, 0)
	for _, aggregate := range aggregates {
		for _, endpoint := range aggregate.ClientEndpoints {
			for _, plan := range endpoint.ProtocolPlans {
				for _, route := range plan.UpstreamPlan.Routes {
					if route.ProviderTarget.ID != endpointID {
						continue
					}
					holders = append(holders, resourcedeletion.Holder{
						Kind:   resourcedeletion.KindEnvironmentRoute,
						ID:     aggregate.ID.String() + "/" + string(route.ID),
						Label:  aggregate.Name,
						Detail: string(route.ID),
					})
				}
			}
		}
	}
	return holders, nil
}
