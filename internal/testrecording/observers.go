// Package testrecording provides synchronous observers for deterministic test
// inspection. Production transports never use these repository adapters.
package testrecording

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type ConnectionReader struct {
	connectionevent.Repository
	epoch *recording.Epoch
}

func Connections(r connectionevent.Repository) *ConnectionReader {
	e, _ := recording.NewEpoch(1)
	return &ConnectionReader{r, e}
}
func (r *ConnectionReader) ScopeStamp(c captureidentity.Reference) recording.Stamp {
	g := uint64(0)
	if c != (captureidentity.Reference{}) {
		g = 1
	}
	t, _ := recording.NewScopeToken(r.epoch, c, recording.Fence{ArchiveEpoch: 1, CaptureGeneration: g})
	return t.Stamp()
}
func (r *ConnectionReader) ObserveConnection(_ recording.Stamp, e connectionevent.Event) recording.OfferResult {
	if _, err := r.Append(context.Background(), e); err != nil {
		return recording.DroppedUnavailable
	}
	return recording.Accepted
}

type EgressWriter struct{ egressaudit.Writer }

func Egress(w egressaudit.Writer) *EgressWriter { return &EgressWriter{w} }
func (w *EgressWriter) ObserveEgress(_ recording.Stamp, a egressaudit.Attempt) recording.OfferResult {
	var err error
	if a.Terminal() {
		_, err = w.Complete(context.Background(), a)
	} else {
		_, err = w.Append(context.Background(), a)
	}
	if err != nil {
		return recording.DroppedUnavailable
	}
	return recording.Accepted
}
func Start(m connectionevent.Runtime, ctx context.Context, a connectionevent.Attempt) (*connectionevent.Connection, error) {
	c, err := m.Start(ctx, a)
	if err != nil {
		return c, err
	}
	return c, c.BindObservationScope(c.ObservationScope(captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "test"}))
}

func (w *EgressWriter) ReportTerminalFailure(err error) {
	if r, ok := w.Writer.(interface{ ReportTerminalFailure(error) }); ok {
		r.ReportTerminalFailure(err)
	}
}
func (w *EgressWriter) ReportRawEvidenceFailure(err error) {
	if r, ok := w.Writer.(interface{ ReportRawEvidenceFailure(error) }); ok {
		r.ReportRawEvidenceFailure(err)
	}
}

func (w *EgressWriter) List(ctx context.Context, q egressaudit.PageRequest) (egressaudit.Page, error) {
	return w.Writer.(egressaudit.Reader).List(ctx, q)
}
