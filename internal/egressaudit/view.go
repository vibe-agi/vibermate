package egressaudit

import "time"

// View is the wire shape of one outbound attempt.
//
// The attempt itself keeps its fields unexported, because it is immutable
// evidence and nothing outside this package may assemble one. That left the
// control API serializing an empty object for every attempt: the endpoint
// that answers "what went out" answered nothing. This is the explicit contract
// that answers it.
//
// Design 06 §4.1 bounds what may appear here. An attempt records where a
// request went and how much crossed, never what it said: there is no URL path,
// header, or body byte in this type, and there is none in the record it is
// built from.
type View struct {
	Sequence     int64         `json:"sequence"`
	ID           string        `json:"id"`
	ConnectionID string        `json:"connectionId,omitempty"`
	Purpose      EgressPurpose `json:"purpose"`
	PayloadClass PayloadClass  `json:"payloadClass"`
	Parent       ParentView    `json:"parent"`
	Caller       CallerKind    `json:"caller"`
	CallerID     string        `json:"callerId,omitempty"`
	// TargetOrigin is scheme, host, and port. It is an origin, not a URL.
	TargetOrigin string       `json:"targetOrigin"`
	Decision     DecisionView `json:"decision"`
	// ReusedTransport says the bytes went out over a connection that already
	// existed, which is why an attempt is not the same fact as a connection.
	ReusedTransport bool      `json:"reusedTransport"`
	StartedAt       time.Time `json:"startedAt"`
	// Terminal says whether this attempt has finished. Until it has, an
	// outcome and a byte count would be guesses.
	Terminal    bool       `json:"terminal"`
	Outcome     Outcome    `json:"outcome,omitempty"`
	ErrorClass  string     `json:"errorClass,omitempty"`
	BytesOut    int64      `json:"bytesOut"`
	BytesIn     int64      `json:"bytesIn"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

type ParentView struct {
	Kind       ParentKind `json:"kind"`
	ID         string     `json:"id,omitempty"`
	ExchangeID string     `json:"exchangeId,omitempty"`
}

type DecisionView struct {
	PolicyID       string              `json:"policyId,omitempty"`
	PolicyRevision uint64              `json:"policyRevision,omitempty"`
	Authority      PolicyAuthorityKind `json:"authority"`
	RuleID         string              `json:"ruleId,omitempty"`
	ProxyID        string              `json:"proxyId,omitempty"`
}

// ViewOf renders one stored attempt for a reader.
func ViewOf(record Record) View {
	attempt := record.Attempt
	view := View{
		Sequence:     record.Sequence,
		ID:           attempt.ID(),
		ConnectionID: attempt.ConnectionID(),
		Purpose:      attempt.Purpose(),
		PayloadClass: attempt.PayloadClass(),
		Parent: ParentView{
			Kind:       attempt.Parent().Kind,
			ID:         attempt.Parent().ID,
			ExchangeID: attempt.Parent().ExchangeID,
		},
		Caller:       attempt.Caller(),
		CallerID:     attempt.CallerID(),
		TargetOrigin: attempt.TargetOrigin(),
		Decision: DecisionView{
			PolicyID:       attempt.Decision().PolicyID,
			PolicyRevision: attempt.Decision().PolicyRevision,
			Authority:      attempt.Decision().Authority,
			RuleID:         attempt.Decision().RuleID,
			ProxyID:        attempt.Decision().ProxyID,
		},
		ReusedTransport: attempt.ReusedTransport(),
		StartedAt:       attempt.StartedAt(),
		Terminal:        attempt.Terminal(),
		Outcome:         attempt.Outcome(),
		ErrorClass:      attempt.ErrorClass(),
		BytesOut:        attempt.BytesOut(),
		BytesIn:         attempt.BytesIn(),
	}
	if !attempt.CompletedAt().IsZero() {
		completed := attempt.CompletedAt()
		view.CompletedAt = &completed
	}
	return view
}

// PageView is what a reader receives.
type PageView struct {
	Items      []View `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func PageViewOf(page Page) PageView {
	view := PageView{
		Items:      make([]View, 0, len(page.Items)),
		NextCursor: page.NextCursor,
	}
	for _, record := range page.Items {
		view.Items = append(view.Items, ViewOf(record))
	}
	return view
}
