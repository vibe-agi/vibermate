package provideraccount

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

type NoteCommand struct {
	ID               ID
	ExpectedRevision uint64
	Note             string
}

func ValidNote(note string) bool {
	if !utf8.ValidString(note) || utf8.RuneCountInString(note) > MaxNoteCharacters || strings.TrimSpace(note) != note {
		return false
	}
	for _, character := range note {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// SetNote changes operator-facing metadata only. It must not invalidate frozen
// routes, alter endpoint grants, or rotate credentials. Empty text clears it.
func (manager *Manager) SetNote(ctx context.Context, command NoteCommand) (View, error) {
	command.Note = strings.TrimSpace(command.Note)
	if ctx == nil || command.ExpectedRevision >= MaxRevision || !ValidNote(command.Note) {
		return View{}, ErrInvalidAccount
	}
	if _, err := NewID(command.ID.String()); err != nil {
		return View{}, err
	}
	if err := manager.setNote(ctx, command); err != nil {
		return View{}, err
	}
	return manager.Get(ctx, command.ID)
}

func (manager *Manager) setNote(ctx context.Context, command NoteCommand) error {
	manager.mu.Lock()
	if manager.closing {
		manager.mu.Unlock()
		return ErrManagerClosing
	}
	account, exists := manager.accounts[command.ID]
	if !exists {
		manager.mu.Unlock()
		return ErrAccountNotFound
	}
	if _, busy := manager.operations[command.ID]; busy {
		manager.mu.Unlock()
		return ErrOperationInProgress
	}
	if account.NoteRevision != command.ExpectedRevision {
		manager.mu.Unlock()
		return ErrRevisionConflict
	}
	if account.Note == command.Note {
		manager.mu.Unlock()
		return nil
	}
	candidate := account
	candidate.Note = command.Note
	candidate.NoteRevision++
	candidate.UpdatedAt = manager.clock.Now().UTC()
	manager.operations[command.ID] = accountOperationNote
	manager.beginInFlightLocked()
	manager.mu.Unlock()
	defer manager.finishOperation(command.ID, accountOperationNote)
	result, err := manager.repository.WriteNote(ctx, command.ExpectedRevision, candidate)
	if result.Outcome != CommitCommitted {
		if result.Outcome == CommitConflict {
			return ErrRevisionConflict
		}
		if err != nil {
			return err
		}
		return ErrInvalidAccount
	}
	if result.Account != candidate {
		return ErrInvalidAccount
	}
	manager.mu.Lock()
	manager.accounts[command.ID] = candidate
	manager.mu.Unlock()
	return nil
}
