package productruntime

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// This child retains the existing fixture's independent assertions on every
// upstream/stored item, response usage, credential and diagnostic count.
func TestRuntimeLongSessionAcceptanceChild(t *testing.T) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	fixture, count := "task5b-4111-4147107", 4111
	policy, err := exchange.DefaultResourcePolicy()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		runtime.ReadMemStats(&after)
		encoded, err := json.Marshal(map[string]any{
			"elapsed_ns": time.Since(start).Nanoseconds(), "total_alloc_bytes": after.TotalAlloc - before.TotalAlloc,
			"policy": policy, "fixture": fixture, "complete_assertions_passed": !t.Failed(),
		})
		if err != nil {
			t.Error(err)
			return
		}
		t.Logf("TASK6_MEASUREMENT %s", encoded)
	}()
	if os.Getenv("TASK6_SHAPE") == "dense" {
		fixture, count = "dense-100000-empty-final-sentinel", 100000
		testRuntimeLongSessionFixture(t, false, "", false, &longSessionAcceptanceOptions{count: count, dense: true, policy: policy})
	} else {
		testRuntimeLongSessionScenario(t, false, "", false)
	}
}

type longSessionAcceptanceOptions struct {
	count          int
	dense          bool
	policy         exchange.ResourcePolicy
	controls       func(*Runtime) error
	httpFour       bool
	concurrentMode *contentBudgetFixtureMode
}

// The diagnostic queries actual constructed Pipeline state without exposing
// a product tuning API or mutating the private configuration.
func (o *longSessionAcceptanceOptions) observePipelineBudget(t *testing.T, pipeline exchangeRuntime) {
	contentBudget := 30 * time.Second
	if o.concurrentMode != nil {
		contentBudget = o.concurrentMode.content
	}
	concrete, ok := pipeline.(*exchange.Pipeline)
	if !ok {
		t.Fatalf("fixture has no actual Pipeline: %T", pipeline)
	}
	for _, budget := range []struct {
		field string
		want  time.Duration
	}{{"observeLimit", 2 * time.Second}, {"contentObserveLimit", contentBudget}} {
		value := reflect.ValueOf(concrete).Elem().FieldByName(budget.field)
		if !value.IsValid() || value.Kind() != reflect.Int64 || time.Duration(value.Int()) != budget.want {
			t.Fatalf("actual %s budget is invalid", budget.field)
		}
		t.Logf("TASK6_ACTUAL_OBSERVATION_BUDGET field=%s ns=%d", budget.field, value.Int())
	}
}

func (o *longSessionAcceptanceOptions) measureBody(t *testing.T, body []byte) {
	lexical, err := protocolcore.MeasureJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	encodedLexical, _ := json.Marshal(map[string]any{"stage": "lexical_cost", "wire_bytes": len(body), "lexical": lexical})
	t.Logf("TASK6_STAGE %s", encodedLexical)
	options := openairesponses.DefaultOptions()
	codec, err := openairesponses.New(options)
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	request, _, err := codec.DecodeClientRequest(body)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	typed, err := protocolcore.MeasureRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{"stage": "standalone_codec_measurement", "wire_bytes": len(body), "nodes": len(request.Messages), "typed": typed, "lexical": lexical, "elapsed_ns": time.Since(start).Nanoseconds(), "total_alloc_bytes": after.TotalAlloc - before.TotalAlloc})
	t.Logf("TASK6_STAGE %s", encoded)
}

type longSessionAcceptanceRecorder struct {
	exchangecontent.Recorder
	sink exchangecontent.SourceRecorder
	t    *testing.T
}

func (r *longSessionAcceptanceRecorder) RecordSource(ctx context.Context, source *exchangecontent.Source) error {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	err := r.sink.RecordSource(ctx, source)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	remaining := int64(-1)
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline).Nanoseconds()
	}
	encoded, _ := json.Marshal(map[string]any{"stage": "manager_store_record_source", "response": source.Metadata().Response != nil, "elapsed_ns": elapsed.Nanoseconds(), "total_alloc_bytes": after.TotalAlloc - before.TotalAlloc, "deadline_remaining_ns": remaining, "error": errorText(err)})
	r.t.Logf("TASK6_STAGE %s", encoded)
	return err
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
