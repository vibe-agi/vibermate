package egressprofile

import (
	"context"
	"errors"
)

type SelectionSource string

const (
	SelectionAccount   SelectionSource = "account"
	SelectionFlow      SelectionSource = "flow"
	SelectionGlobal    SelectionSource = "global"
	SelectionBuiltin   SelectionSource = "builtin"
	SelectionTransport SelectionSource = "transport"
)

type Resolved struct {
	profile         ProfileRevision
	source          SelectionSource
	defaultRevision uint64
}

func (r Resolved) Validate() error {
	if validateSelectionProfile(r.profile) != nil {
		return ErrInvalidProfile
	}
	switch r.source {
	case SelectionAccount, SelectionFlow:
		if r.defaultRevision == 0 {
			return nil
		}
	case SelectionGlobal:
		if r.defaultRevision > 0 && r.defaultRevision <= uint64(MaxRevision) {
			return nil
		}
	case SelectionBuiltin:
		if r.defaultRevision > 0 && r.defaultRevision <= uint64(MaxRevision) && r.profile.Equal(Direct()) {
			return nil
		}
	case SelectionTransport:
		if r.defaultRevision == 0 && r.profile.Equal(Direct()) {
			return nil
		}
	}
	return ErrInvalidProfile
}
func (r Resolved) Profile() ProfileRevision { return r.profile }
func (r Resolved) Source() SelectionSource  { return r.source }
func (r Resolved) DefaultRevision() uint64  { return r.defaultRevision }
func RestoreResolved(profile ProfileRevision, source SelectionSource, revision uint64) (Resolved, error) {
	r := Resolved{profile: profile, source: source, defaultRevision: revision}
	if err := r.Validate(); err != nil {
		return Resolved{}, err
	}
	return r, nil
}

type ProfileRef struct {
	ID       ID
	Revision Revision
}
type DefaultSetting struct {
	Revision uint64
	Profile  *ProfileRevision
}
type SetDefaultCommand struct {
	ExpectedRevision uint64
	Profile          *ProfileRef
}
type DefaultCommitResult struct {
	Outcome CommitOutcome
	Setting DefaultSetting
}
type DefaultRepository interface {
	LoadDefault(context.Context) (DefaultSetting, error)
	WriteDefault(context.Context, uint64, DefaultSetting) (DefaultCommitResult, error)
}
type Resolver interface {
	// Resolve freezes account > flow > global > builtin. Both choices must be
	// valid; explicit winners never depend on the default repository.
	Resolve(context.Context, Choice, Choice) (Resolved, error)
}
type Defaults interface {
	Resolver
	GetDefault(context.Context) (DefaultSetting, error)
	SetDefault(context.Context, SetDefaultCommand) (DefaultSetting, error)
}
type DefaultManager struct {
	repository DefaultRepository
	profiles   Controller
}

var ErrDefaultUnavailable = errors.New("egress default unavailable or corrupt")

func copyDefault(setting DefaultSetting) DefaultSetting {
	if setting.Profile != nil {
		p := *setting.Profile
		setting.Profile = &p
	}
	return setting
}
func validateDefault(setting DefaultSetting) error {
	if setting.Revision == 0 || setting.Revision > uint64(MaxRevision) || setting.Profile != nil && validateSelectionProfile(*setting.Profile) != nil {
		return ErrDefaultUnavailable
	}
	return nil
}
func NewDefaultManager(repository DefaultRepository, profiles Controller) (*DefaultManager, error) {
	if repository == nil || profiles == nil {
		return nil, ErrInvalidProfile
	}
	return &DefaultManager{repository: repository, profiles: profiles}, nil
}
func (m *DefaultManager) Resolve(ctx context.Context, account, flow Choice) (Resolved, error) {
	if m == nil || ctx == nil || account.Validate() != nil || flow.Validate() != nil {
		return Resolved{}, ErrInvalidProfile
	}
	if err := ctx.Err(); err != nil {
		return Resolved{}, err
	}
	if p, ok := account.ExplicitProfile(); ok {
		return RestoreResolved(p, SelectionAccount, 0)
	}
	if p, ok := flow.ExplicitProfile(); ok {
		return RestoreResolved(p, SelectionFlow, 0)
	}
	setting, err := m.GetDefault(ctx)
	if err != nil {
		return Resolved{}, err
	}
	if setting.Profile == nil {
		return RestoreResolved(Direct(), SelectionBuiltin, setting.Revision)
	}
	return RestoreResolved(*setting.Profile, SelectionGlobal, setting.Revision)
}
func (m *DefaultManager) GetDefault(ctx context.Context) (DefaultSetting, error) {
	if m == nil || ctx == nil {
		return DefaultSetting{}, ErrDefaultUnavailable
	}
	setting, err := m.repository.LoadDefault(ctx)
	if err != nil {
		return DefaultSetting{}, err
	}
	if err = validateDefault(setting); err != nil {
		return DefaultSetting{}, err
	}
	return copyDefault(setting), nil
}
func (m *DefaultManager) SetDefault(ctx context.Context, command SetDefaultCommand) (DefaultSetting, error) {
	if m == nil || ctx == nil || command.ExpectedRevision == 0 || command.ExpectedRevision >= uint64(MaxRevision) {
		return DefaultSetting{}, ErrInvalidProfile
	}
	var profile *ProfileRevision
	if command.Profile != nil {
		p, err := m.profiles.GetRevision(ctx, command.Profile.ID, command.Profile.Revision)
		if err != nil {
			return DefaultSetting{}, err
		}
		if validateSelectionProfile(p) != nil {
			return DefaultSetting{}, ErrInvalidProfile
		}
		profile = &p
	}
	current, err := m.GetDefault(ctx)
	if err != nil {
		return DefaultSetting{}, err
	}
	if current.Revision != command.ExpectedRevision {
		return DefaultSetting{}, ErrRevisionConflict
	}
	if current.Profile == nil && profile == nil || current.Profile != nil && profile != nil && current.Profile.Equal(*profile) {
		return current, nil
	}
	candidate := DefaultSetting{Revision: command.ExpectedRevision + 1, Profile: profile}
	result, err := m.repository.WriteDefault(ctx, command.ExpectedRevision, copyDefault(candidate))
	if result.Outcome == CommitConflict {
		return DefaultSetting{}, ErrRevisionConflict
	}
	if err != nil {
		return DefaultSetting{}, err
	}
	if result.Outcome != CommitCommitted || validateDefault(result.Setting) != nil || result.Setting.Revision != candidate.Revision || (result.Setting.Profile == nil) != (candidate.Profile == nil) || candidate.Profile != nil && !candidate.Profile.Equal(*result.Setting.Profile) {
		return DefaultSetting{}, ErrDefaultUnavailable
	}
	return copyDefault(result.Setting), nil
}
