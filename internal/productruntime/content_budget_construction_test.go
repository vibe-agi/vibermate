package productruntime

import (
	"context"
	"crypto/rand"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
)

type contentBudgetFixtureMode struct {
	label                    string
	content, outer, exchange time.Duration
}

// Catches applying race containment to production, omitting it from the test
// Pipeline, changing the ordinary budget, or selecting race via testing.Short.
func testContentBudgetFixtureConstruction(t *testing.T, label string, content, outer, concurrent time.Duration) {
	t.Helper()
	mode := concurrentContentFixtureMode()
	if mode.label != label || mode.content != content || mode.outer != outer || mode.exchange != concurrent {
		t.Fatalf("selected fixture mode=%+v", mode)
	}
	f := newAccountReadFixture(t)
	(&longSessionAcceptanceOptions{}).observePipelineBudget(t, f.runtime.exchanges)
	annotations, err := clientannotation.Open(context.Background(), f.secrets, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer annotations.Destroy()
	decisions, err := toolpolicy.New(f.runtime.approvals)
	if err != nil {
		t.Fatal(err)
	}
	request := exchangeBuildRequest{bodyAdmission: f.runtime.bodyAdmission, ownerContext: context.Background(), actions: f.runtime.offlineHold, accounts: f.runtime.accounts, provider: f.runtime.provider, toolDecisions: decisions, activities: f.runtime.activities, identities: f.runtime.conversationIDs, contents: f.runtime.contents, clock: SystemClock{}, hold: exchange.DefaultHoldPolicy(), annotations: annotations}
	pipeline, err := buildConcurrentContentFixtureExchange(request)
	if err != nil {
		t.Fatal(err)
	}
	defer pipeline.Shutdown(context.Background())
	concrete, ok := pipeline.(*exchange.Pipeline)
	if !ok {
		t.Fatalf("fixture Pipeline=%T", pipeline)
	}
	for _, budget := range []struct {
		field string
		want  time.Duration
	}{{"observeLimit", 2 * time.Second}, {"contentObserveLimit", content}} {
		got := time.Duration(reflect.ValueOf(concrete).Elem().FieldByName(budget.field).Int())
		if got != budget.want {
			t.Errorf("selected %s actual %s=%v want=%v", label, budget.field, got, budget.want)
		}
		t.Logf("selected %s actual %s=%v", label, budget.field, got)
	}
	// Construct again after the test variant to detect shared/global mutation.
	production, err := buildExchange(request)
	if err != nil {
		t.Fatal(err)
	}
	defer production.Shutdown(context.Background())
	(&longSessionAcceptanceOptions{}).observePipelineBudget(t, production)
}
