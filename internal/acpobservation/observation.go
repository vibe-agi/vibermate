// Package acpobservation projects a bounded, explicitly incomplete view of ACP.
// It never implements an Agent, answers permissions, or infers HTTP usage.
package acpobservation

import (
	"context"
	"errors"
	"math"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
)

const (
	MaxFrameBytes    = 1 << 20
	MaxSessions      = 64
	MaxPrompts       = 256
	MaxTextBytes     = 128 << 10
	MaxSnapshotBytes = 768 << 10
)

var (
	ErrInvalid  = errors.New("invalid ACP observation")
	ErrNotFound = errors.New("ACP observation not found")
	ErrConflict = errors.New("ACP observation revision conflict")
)

type Agent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Session struct {
	ID        string `json:"id"`
	CWD       string `json:"cwd"`
	Operation string `json:"operation"`
}

type Prompt struct {
	Sequence   uint64 `json:"sequence"`
	SessionID  string `json:"sessionId"`
	State      string `json:"state"`
	StopReason string `json:"stopReason,omitempty"`
	UserText   string `json:"userText,omitempty"`
	AgentText  string `json:"agentText,omitempty"`
	ToolCalls  uint64 `json:"toolCalls"`
}

// Snapshot is a projection, not a wire transcript. Agent identity/version and
// session directories are self-reported. Only prompt text and visible assistant
// text can be retained; auth, stderr, error.data and arbitrary extensions cannot.
type Snapshot struct {
	Revision            uint64    `json:"revision"`
	ProtocolVersion     uint32    `json:"protocolVersion"`
	Agent               Agent     `json:"agent"`
	NeedsAuthentication bool      `json:"needsAuthentication,omitempty"`
	Sessions            []Session `json:"sessions"`
	Prompts             []Prompt  `json:"prompts"`
	Incomplete          bool      `json:"incomplete"`
	Final               bool      `json:"final"`
	ExitCode            *int      `json:"exitCode,omitempty"`
}

type Record struct {
	RunID           string                             `json:"runId"`
	Policy          environment.ContentRecordingPolicy `json:"policy"`
	ExpiresAtMillis int64                              `json:"expiresAtMillis"`
	Snapshot        Snapshot                           `json:"snapshot"`
	Expired         bool                               `json:"expired"`
}

// Repository mutations require an already-authorized live Capture Run. Its
// immutable policy cannot be upgraded by a later snapshot. Equal revisions are
// idempotent only if the entire snapshot agrees; final snapshots are immutable.
type Repository interface {
	Create(context.Context, Record, int64) error
	Save(context.Context, string, Snapshot, int64) error
	Read(context.Context, string, int64) (Record, error)
	// Markers reads only connection membership, never prompt content.
	Markers(context.Context, []string, int64) (map[string]bool, error)
}

func textValid(value string, limit int) bool { return len(value) <= limit && utf8.ValidString(value) }

func (snapshot Snapshot) Validate(content bool) error {
	if snapshot.Revision == 0 || snapshot.Revision > math.MaxInt64 || len(snapshot.Sessions) > MaxSessions || len(snapshot.Prompts) > MaxPrompts ||
		!textValid(snapshot.Agent.Name, 256) || !textValid(snapshot.Agent.Version, 128) ||
		(snapshot.Final != (snapshot.ExitCode != nil)) || (snapshot.ExitCode != nil && (*snapshot.ExitCode < 0 || *snapshot.ExitCode > 255)) {
		return ErrInvalid
	}
	sessions := map[string]bool{}
	for _, session := range snapshot.Sessions {
		if session.ID == "" || !textValid(session.ID, 512) || !textValid(session.CWD, 4096) || sessions[session.ID] {
			return ErrInvalid
		}
		switch session.Operation {
		case "new", "load", "resume", "fork":
		default:
			return ErrInvalid
		}
		sessions[session.ID] = true
	}
	total := 0
	var previous uint64
	for _, prompt := range snapshot.Prompts {
		if prompt.Sequence <= previous || !sessions[prompt.SessionID] || !textValid(prompt.UserText, MaxTextBytes) || !textValid(prompt.AgentText, MaxTextBytes) {
			return ErrInvalid
		}
		previous = prompt.Sequence
		switch prompt.State {
		case "pending", "completed", "cancelled", "failed", "interrupted":
		default:
			return ErrInvalid
		}
		if snapshot.Final && prompt.State == "pending" {
			return ErrInvalid
		}
		switch prompt.StopReason {
		case "", "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled", "other":
		default:
			return ErrInvalid
		}
		total += len(prompt.UserText) + len(prompt.AgentText)
	}
	if total > MaxTextBytes || (!content && total != 0) {
		return ErrInvalid
	}
	return nil
}
