package runtimepersistence

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReferenceSchemaCacheReusesActualDDLAndDefendsReturnedValues(t *testing.T) {
	var cache referenceSchemaCache
	calls := 0
	materialize := func(ctx context.Context, v referenceSchemaVariant) ([]schemaObject, error) {
		calls++
		return materializeReferenceSchema(ctx, v)
	}
	first, err := cache.read(t.Context(), referenceObservations, materialize)
	if err != nil || len(first) == 0 {
		t.Fatal("cold actual DDL", err)
	}
	expected := slices.Clone(first)
	first[0].statement = "caller mutation"
	second, err := cache.read(t.Context(), referenceObservations, materialize)
	if err != nil || !slices.Equal(second, expected) || calls != 1 {
		t.Fatalf("warm reuse/defensive ownership: calls=%d err=%v", calls, err)
	}
}

func TestReferenceSchemaCacheCancellationAndFailureNeverPoison(t *testing.T) {
	var cache referenceSchemaCache
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := cache.read(canceled, referenceV1, materializeReferenceSchema); !errors.Is(err, context.Canceled) {
		t.Fatal("cold cancellation ignored", err)
	}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	partial := errors.New("failure after materializing actual DDL")
	for _, scenario := range []string{"partial", "cancel-before-publication"} {
		_, err := cache.read(ctx, referenceV1, func(ctx context.Context, v referenceSchemaVariant) ([]schemaObject, error) {
			objects, err := materializeReferenceSchema(ctx, v)
			if err != nil {
				return nil, err
			}
			if scenario == "partial" {
				return objects[:1], partial
			}
			stop()
			return objects, nil
		})
		if scenario == "partial" && !errors.Is(err, partial) || scenario != "partial" && !errors.Is(err, context.Canceled) {
			t.Fatalf("failed build admitted: %s %v", scenario, err)
		}
	}
	complete, err := cache.read(t.Context(), referenceV1, materializeReferenceSchema)
	actual, actualErr := materializeReferenceSchema(t.Context(), referenceV1)
	if err != nil || actualErr != nil || !slices.Equal(complete, actual) {
		t.Fatal("failure poisoned later full materialization", err, actualErr)
	}
	if value, err := cache.read(canceled, referenceV1, materializeReferenceSchema); !errors.Is(err, context.Canceled) || len(value) != 0 {
		t.Fatal("warm cache ignored caller cancellation", err)
	}
}

func TestReferenceSchemaCacheConcurrentMissesDoNotSerializeSQLite(t *testing.T) {
	var cache referenceSchemaCache
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		_, err := cache.read(t.Context(), referenceV1, func(ctx context.Context, v referenceSchemaVariant) ([]schemaObject, error) {
			objects, err := materializeReferenceSchema(ctx, v)
			close(entered)
			<-release
			return objects, err
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("cold DDL not entered")
	}
	otherDone := make(chan error, 1)
	go func() {
		other, err := cache.read(t.Context(), referenceObservations, materializeReferenceSchema)
		if err == nil && len(other) == 0 {
			err = errors.New("empty actual reference schema")
		}
		otherDone <- err
	}()
	select {
	case err := <-otherDone:
		if err != nil {
			t.Fatal("unrelated shape failed", err)
		}
	case <-time.After(10 * time.Second):
		unblock()
		<-done
		<-otherDone
		t.Fatal("cache lock serialized an unrelated shape behind materialization")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var builds atomic.Int32
	build := func(ctx context.Context, v referenceSchemaVariant) ([]schemaObject, error) {
		builds.Add(1)
		return materializeReferenceSchema(ctx, v)
	}
	var group sync.WaitGroup
	results := make(chan []schemaObject, 8)
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			value, err := cache.read(t.Context(), referenceV2, build)
			if err != nil {
				t.Error(err)
			}
			results <- value
		}()
	}
	group.Wait()
	close(results)
	var expected []schemaObject
	for value := range results {
		if expected == nil {
			expected = slices.Clone(value)
		}
		if !slices.Equal(value, expected) {
			t.Fatal("concurrent publication changed schema")
		}
		if len(value) > 0 {
			value[0].name = "private returned mutation"
		}
	}
	before := builds.Load()
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			warm, err := cache.read(t.Context(), referenceV2, build)
			if err != nil || !slices.Equal(warm, expected) {
				t.Error("concurrent warm reuse changed immutable objects", err)
				return
			}
			warm[0].name = "independent warm caller mutation"
		}()
	}
	group.Wait()
	if builds.Load() != before {
		t.Fatal("concurrent warm reads rebuilt a published reference")
	}
}

func TestReferenceSchemaCacheBoundsKnownVariants(t *testing.T) {
	var cache referenceSchemaCache
	for v := referenceSchemaVariant(0); v < referenceSchemaVariantCount; v++ {
		objects, err := cache.read(t.Context(), v, materializeReferenceSchema)
		if err != nil || len(objects) == 0 {
			t.Fatalf("compiled schema %d: %v", v, err)
		}
	}
	called := false
	if _, err := cache.read(t.Context(), referenceSchemaVariantCount, func(context.Context, referenceSchemaVariant) ([]schemaObject, error) { called = true; return nil, nil }); !errors.Is(err, ErrUnsupportedSchema) || called {
		t.Fatal("unbounded schema variant admitted", err)
	}
}

func TestInspectionReferenceCacheStillChecksActualSourceSchema(t *testing.T) {
	options, current := inspectionSourcesFixture(t)
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	lease, err := catalog.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	lease.close()
	nativeExec(t, current.store.database, `CREATE TABLE unexpected_current_schema(value TEXT)`)
	if lease, err := catalog.acquire(t.Context()); !errors.Is(err, ErrUnsupportedSchema) {
		if lease != nil {
			lease.close()
		}
		t.Fatal("warm reference cache admitted actual source tampering", err)
	}
	nativeExec(t, current.store.database, `DROP TABLE unexpected_current_schema`)
	lease, err = catalog.acquire(t.Context())
	if err != nil {
		t.Fatal("source refusal poisoned later valid inspection", err)
	}
	lease.close()
}
