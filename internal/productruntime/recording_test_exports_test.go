package productruntime

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// External launcher integration tests cannot import runlauncher into this
// package: launcher already imports Runtime. These test-only exports compose
// the real general recorder/compiler without adding a production constructor.
type ACPRecordingTestRecorder interface {
	acpobservation.Recorder
	recording.ScopeIssuer
	Flush(context.Context) error
	Shutdown(context.Context) error
}

func NewACPRecordingTestRecorder(ctx context.Context, writer acpobservation.Writer) ACPRecordingTestRecorder {
	budget, _ := recording.NewBudget(recording.Limits{Records: 2048, Bytes: 64 << 20})
	recorder := newObservationRecorder(ctx, budget, nil, nil)
	recorder.acp = writer
	return recorder
}
func NewRecordingTestCompiler() (environment.Compiler, error) {
	return productionEnvironmentCompiler(nil, nil)
}
