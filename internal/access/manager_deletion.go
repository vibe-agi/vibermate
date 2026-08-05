package access

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

func (m *Manager) PreviewDeleteAccess(
	ctx context.Context,
	command PreviewDeletionCommand,
) (DeletionPreview, error) {
	if ctx == nil || command.validate() != nil {
		return DeletionPreview{}, newFailure(
			ReasonInvalidAccess,
			ErrInvalidAccess,
			command.AccessID,
			command.ExpectedRevision,
			0,
			nil,
		)
	}
	operationContext, finish, err := m.lifecycle.begin(ctx)
	if err != nil {
		return DeletionPreview{}, deletionLifecycleFailure(command, err)
	}
	defer finish()
	if err := m.lockWriter(operationContext); err != nil {
		return DeletionPreview{}, deletionLifecycleFailure(command, err)
	}
	defer m.unlockWriter()

	preview, err := m.deletionPreviewLocked(operationContext, command)
	if err != nil {
		return DeletionPreview{}, err
	}
	return preview, nil
}

func (m *Manager) DeleteAccess(
	ctx context.Context,
	command DeleteCommand,
) (DeleteResult, error) {
	if ctx == nil || command.validate() != nil {
		return DeleteResult{Outcome: DeleteOutcomeNotCommitted}, newFailure(
			ReasonInvalidAccess,
			ErrInvalidAccess,
			command.AccessID,
			command.ExpectedRevision,
			0,
			nil,
		)
	}
	operationContext, finish, err := m.lifecycle.begin(ctx)
	if err != nil {
		return DeleteResult{Outcome: DeleteOutcomeNotCommitted},
			deletionLifecycleFailure(PreviewDeletionCommand{
				AccessID:         command.AccessID,
				ExpectedRevision: command.ExpectedRevision,
			}, err)
	}
	defer finish()
	if err := m.lockWriter(operationContext); err != nil {
		return DeleteResult{Outcome: DeleteOutcomeNotCommitted},
			deletionLifecycleFailure(PreviewDeletionCommand{
				AccessID:         command.AccessID,
				ExpectedRevision: command.ExpectedRevision,
			}, err)
	}
	defer m.unlockWriter()

	inspection, exists, err := m.repository.InspectDeletion(
		operationContext,
		command.AccessID,
		command.DeletedAt,
	)
	if err != nil {
		return DeleteResult{Outcome: DeleteOutcomeNotCommitted}, newFailure(
			ReasonDeletionNotCommitted,
			ErrAccessDeletionNotCommitted,
			command.AccessID,
			command.ExpectedRevision,
			0,
			err,
		)
	}
	if !exists {
		result, deleteErr := m.repository.Delete(operationContext, DeleteMutation{
			AccessID:                 command.AccessID,
			ExpectedRevision:         command.ExpectedRevision,
			ExpectedRepositoryImpact: command.ExpectedImpactToken,
			RetireWorkspaceBindings:  command.RetireWorkspaceBindings,
			DeletedAt:                command.DeletedAt,
		})
		if deleteErr == nil && result.Outcome == DeleteOutcomeRetired {
			if result.Revision == command.ExpectedRevision {
				return result, nil
			}
			return DeleteResult{
					Outcome:  DeleteOutcomeConflict,
					Revision: result.Revision,
				}, newFailure(
					ReasonRevisionConflict,
					ErrRevisionConflict,
					command.AccessID,
					command.ExpectedRevision,
					result.Revision,
					nil,
				)
		}
		if deleteErr == nil && result.Outcome == DeleteOutcomeNotConfigured {
			return result, newFailure(
				ReasonAccessNotConfigured,
				ErrAccessNotConfigured,
				command.AccessID,
				command.ExpectedRevision,
				0,
				nil,
			)
		}
		return DeleteResult{
				Outcome:  DeleteOutcomeIndeterminate,
				Revision: result.Revision,
			}, newFailure(
				ReasonCommitOutcomeUnknown,
				ErrCommitOutcomeUnknown,
				command.AccessID,
				command.ExpectedRevision,
				result.Revision,
				deleteErr,
			)
	}
	if inspection.Aggregate.Binding.Revision != command.ExpectedRevision {
		return DeleteResult{
				Outcome:  DeleteOutcomeConflict,
				Revision: inspection.Aggregate.Binding.Revision,
			}, newFailure(
				ReasonRevisionConflict,
				ErrRevisionConflict,
				command.AccessID,
				command.ExpectedRevision,
				inspection.Aggregate.Binding.Revision,
				nil,
			)
	}
	aggregates, err := m.repository.LoadAll(operationContext)
	if err != nil {
		return DeleteResult{Outcome: DeleteOutcomeNotCommitted}, newFailure(
			ReasonDeletionNotCommitted,
			ErrAccessDeletionNotCommitted,
			command.AccessID,
			command.ExpectedRevision,
			0,
			err,
		)
	}
	preview, secretImpact, err := deletionPreview(inspection, aggregates)
	if err != nil {
		return DeleteResult{Outcome: DeleteOutcomeNotCommitted}, newFailure(
			ReasonDeletionNotCommitted,
			ErrAccessDeletionNotCommitted,
			command.AccessID,
			command.ExpectedRevision,
			inspection.Aggregate.Binding.Revision,
			err,
		)
	}
	if preview.ImpactToken != command.ExpectedImpactToken {
		return DeleteResult{
				Outcome:  DeleteOutcomeImpactChanged,
				Revision: preview.Revision,
			}, newFailure(
				ReasonDeletionChanged,
				ErrAccessDeletionChanged,
				command.AccessID,
				command.ExpectedRevision,
				preview.Revision,
				nil,
			)
	}
	if !preview.CanDelete(command.RetireWorkspaceBindings) {
		return DeleteResult{
				Outcome:  DeleteOutcomeBlocked,
				Revision: preview.Revision,
			}, newFailure(
				ReasonDeletionBlocked,
				ErrAccessDeletionBlocked,
				command.AccessID,
				command.ExpectedRevision,
				preview.Revision,
				nil,
			)
	}
	if _, resolveErr := m.projection.ResolveAccess(command.AccessID); errors.Is(
		resolveErr,
		ErrProjectionUnavailable,
	) {
		return DeleteResult{
				Outcome:  DeleteOutcomeBlocked,
				Revision: preview.Revision,
			}, newFailure(
				ReasonProjectionUnavailable,
				ErrProjectionUnavailable,
				command.AccessID,
				command.ExpectedRevision,
				preview.Revision,
				resolveErr,
			)
	}

	fence, err := m.requests.beginDeletion(operationContext, command.AccessID)
	if err != nil {
		return DeleteResult{
				Outcome:  DeleteOutcomeNotCommitted,
				Revision: preview.Revision,
			}, newFailure(
				ReasonDeletionNotCommitted,
				ErrAccessDeletionNotCommitted,
				command.AccessID,
				command.ExpectedRevision,
				preview.Revision,
				err,
			)
	}
	committedFence := false
	defer func() {
		if !committedFence {
			fence.Abort()
		}
	}()
	for _, reference := range secretImpact.Exclusive {
		if err := m.secrets.RetireSecret(operationContext, reference); err != nil {
			return DeleteResult{
					Outcome:  DeleteOutcomeNotCommitted,
					Revision: preview.Revision,
				}, newFailure(
					ReasonDeletionNotCommitted,
					ErrAccessDeletionNotCommitted,
					command.AccessID,
					command.ExpectedRevision,
					preview.Revision,
					err,
				)
		}
	}
	repositoryImpact, err := inspection.RepositoryImpactToken()
	if err != nil {
		return DeleteResult{
			Outcome:  DeleteOutcomeNotCommitted,
			Revision: preview.Revision,
		}, err
	}
	result, deleteErr := m.repository.Delete(operationContext, DeleteMutation{
		AccessID:                 command.AccessID,
		ExpectedRevision:         command.ExpectedRevision,
		ExpectedRepositoryImpact: repositoryImpact,
		RetireWorkspaceBindings:  command.RetireWorkspaceBindings,
		DeletedAt:                command.DeletedAt,
	})
	switch result.Outcome {
	case DeleteOutcomeCommitted, DeleteOutcomeRetired:
		if deleteErr != nil || result.Revision != preview.Revision {
			m.projection.MarkUnavailable(command.AccessID)
			fence.Commit()
			committedFence = true
			return DeleteResult{
					Outcome:  DeleteOutcomeIndeterminate,
					Revision: result.Revision,
				}, newFailure(
					ReasonCommitOutcomeUnknown,
					ErrCommitOutcomeUnknown,
					command.AccessID,
					command.ExpectedRevision,
					result.Revision,
					errors.Join(deleteErr, ErrInvalidRepositoryState),
				)
		}
		fence.Commit()
		committedFence = true
		return DeleteResult{
			Outcome:  DeleteOutcomeCommitted,
			Revision: result.Revision,
		}, nil
	case DeleteOutcomeIndeterminate:
		m.projection.MarkUnavailable(command.AccessID)
		fence.Commit()
		committedFence = true
		return result, newFailure(
			ReasonCommitOutcomeUnknown,
			ErrCommitOutcomeUnknown,
			command.AccessID,
			command.ExpectedRevision,
			result.Revision,
			deleteErr,
		)
	case DeleteOutcomeConflict:
		return result, newFailure(
			ReasonRevisionConflict,
			ErrRevisionConflict,
			command.AccessID,
			command.ExpectedRevision,
			result.Revision,
			deleteErr,
		)
	case DeleteOutcomeImpactChanged:
		return result, newFailure(
			ReasonDeletionChanged,
			ErrAccessDeletionChanged,
			command.AccessID,
			command.ExpectedRevision,
			result.Revision,
			deleteErr,
		)
	case DeleteOutcomeBlocked:
		return result, newFailure(
			ReasonDeletionBlocked,
			ErrAccessDeletionBlocked,
			command.AccessID,
			command.ExpectedRevision,
			result.Revision,
			deleteErr,
		)
	case DeleteOutcomeNotConfigured:
		return result, newFailure(
			ReasonAccessNotConfigured,
			ErrAccessNotConfigured,
			command.AccessID,
			command.ExpectedRevision,
			result.Revision,
			deleteErr,
		)
	case DeleteOutcomeNotCommitted:
		return result, newFailure(
			ReasonDeletionNotCommitted,
			ErrAccessDeletionNotCommitted,
			command.AccessID,
			command.ExpectedRevision,
			result.Revision,
			deleteErr,
		)
	default:
		m.projection.MarkUnavailable(command.AccessID)
		fence.Commit()
		committedFence = true
		return DeleteResult{
				Outcome:  DeleteOutcomeIndeterminate,
				Revision: result.Revision,
			}, newFailure(
				ReasonCommitOutcomeUnknown,
				ErrCommitOutcomeUnknown,
				command.AccessID,
				command.ExpectedRevision,
				result.Revision,
				fmt.Errorf("%w: outcome=%q", ErrInvalidRepositoryState, result.Outcome),
			)
	}
}

func (m *Manager) deletionPreviewLocked(
	ctx context.Context,
	command PreviewDeletionCommand,
) (DeletionPreview, error) {
	inspection, exists, err := m.repository.InspectDeletion(
		ctx,
		command.AccessID,
		command.ObservedAt,
	)
	if err != nil {
		return DeletionPreview{}, err
	}
	if !exists {
		return DeletionPreview{}, newFailure(
			ReasonAccessNotConfigured,
			ErrAccessNotConfigured,
			command.AccessID,
			command.ExpectedRevision,
			0,
			nil,
		)
	}
	actualRevision := inspection.Aggregate.Binding.Revision
	if actualRevision != command.ExpectedRevision {
		return DeletionPreview{}, newFailure(
			ReasonRevisionConflict,
			ErrRevisionConflict,
			command.AccessID,
			command.ExpectedRevision,
			actualRevision,
			nil,
		)
	}
	aggregates, err := m.repository.LoadAll(ctx)
	if err != nil {
		return DeletionPreview{}, err
	}
	preview, _, err := deletionPreview(inspection, aggregates)
	return preview, err
}

func deletionPreview(
	inspection DeletionInspection,
	aggregates []Aggregate,
) (DeletionPreview, DeletionSecretImpact, error) {
	if err := inspection.Validate(); err != nil {
		return DeletionPreview{}, DeletionSecretImpact{}, err
	}
	found := false
	for _, aggregate := range aggregates {
		if aggregate.Binding.ID == inspection.Aggregate.Binding.ID {
			if !aggregate.Equal(inspection.Aggregate) {
				return DeletionPreview{}, DeletionSecretImpact{},
					ErrInvalidRepositoryState
			}
			found = true
		}
	}
	if !found {
		return DeletionPreview{}, DeletionSecretImpact{}, ErrInvalidRepositoryState
	}
	secretImpact := classifyDeletionSecrets(
		inspection.Aggregate.Binding.ID,
		aggregates,
	)
	token, err := deletionImpactToken(inspection, secretImpact)
	if err != nil {
		return DeletionPreview{}, DeletionSecretImpact{}, err
	}
	blockers := make([]DeletionBlocker, 0, 4)
	if inspection.Aggregate.Binding.Status != AccessStatusDisabled {
		blockers = append(blockers, DeletionBlockerDisableFirst)
	}
	activeRuns := inspection.activeCaptureRunCount()
	if activeRuns != 0 {
		blockers = append(blockers, DeletionBlockerActiveCaptureRuns)
	}
	if len(inspection.WorkspaceReferences) != 0 {
		blockers = append(blockers, DeletionBlockerConfirmWorkspaceRetirement)
	}
	if len(inspection.ProxyClientReferences) != 0 {
		blockers = append(blockers, DeletionBlockerProxyClientBindings)
	}
	return DeletionPreview{
		AccessID:                inspection.Aggregate.Binding.ID,
		Name:                    inspection.Aggregate.Binding.Name,
		Revision:                inspection.Aggregate.Binding.Revision,
		Status:                  inspection.Aggregate.Binding.Status,
		WorkspaceBindingCount:   len(inspection.WorkspaceReferences),
		ActiveCaptureRunCount:   activeRuns,
		ProxyClientBindingCount: len(inspection.ProxyClientReferences),
		ExclusiveSecretCount:    len(secretImpact.Exclusive),
		SharedSecretCount:       len(secretImpact.Shared),
		ImpactToken:             token,
		Blockers:                blockers,
	}, secretImpact, nil
}

func classifyDeletionSecrets(
	target AccessID,
	aggregates []Aggregate,
) DeletionSecretImpact {
	owned := make(map[string]SecretRef)
	usedElsewhere := make(map[string]struct{})
	for _, aggregate := range aggregates {
		seen := make(map[string]struct{})
		for _, account := range aggregate.AccountBindings {
			key := account.SecretRef.String()
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			if aggregate.Binding.ID == target {
				owned[key] = account.SecretRef
			} else {
				usedElsewhere[key] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(owned))
	for key := range owned {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	impact := DeletionSecretImpact{
		Exclusive: make([]SecretRef, 0, len(keys)),
		Shared:    make([]SecretRef, 0, len(keys)),
	}
	for _, key := range keys {
		if _, shared := usedElsewhere[key]; shared {
			impact.Shared = append(impact.Shared, owned[key])
		} else {
			impact.Exclusive = append(impact.Exclusive, owned[key])
		}
	}
	return impact
}

func (m *Manager) lockWriter(ctx context.Context) error {
	select {
	case <-m.writer:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (m *Manager) unlockWriter() {
	m.writer <- struct{}{}
}

func deletionLifecycleFailure(
	command PreviewDeletionCommand,
	err error,
) error {
	if errors.Is(err, ErrAccessRuntimeStopping) {
		return newFailure(
			ReasonAccessRuntimeStopping,
			ErrAccessRuntimeStopping,
			command.AccessID,
			command.ExpectedRevision,
			0,
			err,
		)
	}
	return newFailure(
		ReasonDeletionNotCommitted,
		ErrAccessDeletionNotCommitted,
		command.AccessID,
		command.ExpectedRevision,
		0,
		err,
	)
}
