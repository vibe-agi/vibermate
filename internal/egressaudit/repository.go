package egressaudit

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultPageLimit bounds an unspecified page so a reader cannot ask the
	// store for an unbounded result set.
	DefaultPageLimit = 50
	// MaxPageLimit bounds any page.
	MaxPageLimit = 500
)

// PageRequest selects immutable attempts. Filters are exact matches on the
// typed fields; there is no free-form query surface.
type PageRequest struct {
	Limit        int
	AfterCursor  string
	ConnectionID string
	ExchangeID   string
	ParentKind   ParentKind
	ParentID     string
	Purpose      EgressPurpose
}

func (request PageRequest) Normalized() (PageRequest, error) {
	if request.Limit == 0 {
		request.Limit = DefaultPageLimit
	}
	if request.Limit < 0 || request.Limit > MaxPageLimit {
		return PageRequest{}, errors.New("egress page limit is out of range")
	}
	if request.ParentKind != "" && request.ParentID == "" {
		return PageRequest{}, errors.New(
			"an egress parent filter requires both a kind and an ID",
		)
	}
	if request.ParentID != "" && request.ParentKind == "" {
		return PageRequest{}, errors.New(
			"an egress parent filter requires both a kind and an ID",
		)
	}
	if request.ExchangeID != "" {
		if err := validateIdentity("Exchange ID", request.ExchangeID); err != nil {
			return PageRequest{}, err
		}
	}
	if request.Purpose != "" {
		if _, err := AuthorityForPurpose(request.Purpose); err != nil {
			return PageRequest{}, err
		}
	}
	return request, nil
}

// Record is one persisted attempt plus its store sequence.
type Record struct {
	Sequence int64
	Attempt  Attempt
}

type Page struct {
	Items      []Record
	NextCursor string
}

// Repository persists immutable attempts. An attempt is appended once when the
// outbound begins and completed once when it reaches a terminal, so a late
// writer can never rewrite an earlier destination.
type Repository interface {
	Append(context.Context, Attempt) (Record, error)
	Complete(context.Context, Attempt) (Record, error)
	List(context.Context, PageRequest) (Page, error)
	// Recover terminalizes attempts left in flight by a previous daemon
	// incarnation. It is a startup-only ownership operation and is deliberately
	// absent from Writer so data-plane callers cannot invoke it.
	Recover(context.Context, time.Time) (int, error)
}

// Writer is the append side a transport depends on. It is narrower than the
// repository so a data-plane component cannot read the audit log.
type Writer interface {
	Append(context.Context, Attempt) (Record, error)
	Complete(context.Context, Attempt) (Record, error)
}

// Reader is the read-only projection used by control surfaces.
type Reader interface {
	List(context.Context, PageRequest) (Page, error)
}

var ErrInvalidCursor = errors.New("egress cursor is invalid")

// Cursor encodes a store sequence for keyset pagination.
func Cursor(sequence int64) (string, error) {
	if sequence <= 0 {
		return "", ErrInvalidCursor
	}
	return base64.RawURLEncoding.EncodeToString(
		[]byte("v1:" + strconv.FormatInt(sequence, 10)),
	), nil
}

func ParseCursor(value string) (int64, error) {
	if value == "" ||
		len(value) > 128 ||
		strings.ContainsAny(value, " \t\r\n=") {
		return 0, ErrInvalidCursor
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, ErrInvalidCursor
	}
	payload, found := strings.CutPrefix(string(decoded), "v1:")
	if !found {
		return 0, ErrInvalidCursor
	}
	sequence, err := strconv.ParseInt(payload, 10, 64)
	if err != nil || sequence <= 0 {
		return 0, ErrInvalidCursor
	}
	return sequence, nil
}
