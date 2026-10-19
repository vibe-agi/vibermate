package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync"
)

type referenceSchemaVariant uint8

const (
	referenceV1 referenceSchemaVariant = iota
	referenceV2
	referenceMigratedV2
	referenceV3
	referenceV2ToV3
	referenceV1ToV3
	referenceObservations
	referenceSchemaVariantCount
)

func referenceSchemaDDL(variant referenceSchemaVariant) (string, error) {
	switch variant {
	case referenceV1:
		return schemaV1SQL, nil
	case referenceV2:
		return schemaV2SQL, nil
	case referenceMigratedV2:
		return schemaV1SQL + "\n" + migration001To002SQL, nil
	case referenceV3:
		return schemaRuntimeV3SQL, nil
	case referenceV2ToV3:
		return schemaV2SQL + "\n" + migration002To003SQL, nil
	case referenceV1ToV3:
		return schemaV1SQL + "\n" + migration001To002SQL + "\n" + migration002To003SQL, nil
	case referenceObservations:
		return observationSchemaSQL, nil
	default:
		return "", ErrUnsupportedSchema
	}
}

type referenceSchemaEntry struct {
	mu      sync.RWMutex
	objects []schemaObject
}
type referenceSchemaCache struct {
	entries [referenceSchemaVariantCount]referenceSchemaEntry
}

var expectedObservationSchemas referenceSchemaCache

// The materializer is supplied by the private caller and never retained. Its
// only production implementation constructs one of the fixed compiled DDLs.
func (c *referenceSchemaCache) read(ctx context.Context, variant referenceSchemaVariant, materialize func(context.Context, referenceSchemaVariant) ([]schemaObject, error)) ([]schemaObject, error) {
	if ctx == nil {
		return nil, ErrInspectionUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if variant >= referenceSchemaVariantCount {
		return nil, ErrUnsupportedSchema
	}
	entry := &c.entries[variant]
	entry.mu.RLock()
	cached := slices.Clone(entry.objects)
	entry.mu.RUnlock()
	if cached != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return cached, nil
	}
	// Concurrent cold misses may build duplicate bounded references. No cache
	// mutex covers SQLite I/O, and no in-flight task or SQL handle is retained.
	objects, err := materialize(ctx, variant)
	if err != nil {
		return nil, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if entry.objects == nil {
		entry.objects = slices.Clone(objects)
	}
	return slices.Clone(entry.objects), nil
}

func materializeReferenceSchema(ctx context.Context, variant referenceSchemaVariant) ([]schemaObject, error) {
	ddl, err := referenceSchemaDDL(variant)
	if err != nil {
		return nil, err
	}
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	reference.SetMaxOpenConns(1)
	if _, err = reference.ExecContext(ctx, ddl); err != nil {
		return nil, errors.Join(err, reference.Close())
	}
	objects, err := schemaObjects(ctx, reference)
	return objects, errors.Join(err, reference.Close())
}
