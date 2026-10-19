package nativetool

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"time"
)

// TransitionalRepository is a compile-only bridge for composition awaiting the
// content/inspection split. Authority.Get always wins. Optional dependencies
// may be nil and never open storage or prevent construction.
type TransitionalRepository struct {
	AuthorityRepository
	Inspection interface {
		Page(context.Context, PageRequest) (Page, error)
	}
	Content interface {
		PutContent(context.Context, ID, string, []byte, time.Time) error
		ReadContent(context.Context, captureidentity.Reference, ID, string, int, int) ([]byte, error)
	}
}

func (r TransitionalRepository) Page(ctx context.Context, q PageRequest) (Page, error) {
	if r.Inspection == nil {
		return Page{}, ErrUnavailable
	}
	return r.Inspection.Page(ctx, q)
}
func (r TransitionalRepository) PutContent(ctx context.Context, id ID, part string, body []byte, at time.Time) error {
	if r.Content == nil {
		return ErrUnavailable
	}
	return r.Content.PutContent(ctx, id, part, body, at)
}
func (r TransitionalRepository) ReadContent(ctx context.Context, c captureidentity.Reference, id ID, part string, offset, limit int) ([]byte, error) {
	if r.Content == nil {
		return nil, ErrUnavailable
	}
	return r.Content.ReadContent(ctx, c, id, part, offset, limit)
}

var _ Repository = TransitionalRepository{}
