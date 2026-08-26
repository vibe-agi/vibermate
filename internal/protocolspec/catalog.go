package protocolspec

import (
	"errors"
	"fmt"
	"slices"
	"sort"
)

var (
	ErrUnknownDialect       = errors.New("protocol dialect is not in the catalog")
	ErrUnsupportedCodecPair = errors.New("codec pair is not in the catalog")
)

// CodecPairDefinition is immutable catalog input. ClientOperationIDs names
// the semantic operations the codec pair can translate; the catalog retains
// every operation for a client dialect separately for request admission.
type CodecPairDefinition struct {
	ID                   CodecPairID
	Revision             Revision
	ClientDialect        Dialect
	ProviderDialect      Dialect
	ClientOperationIDs   []ClientOperationID
	RequiredCapabilities []ProviderCapability
}

type codecPairKey struct {
	client   Dialect
	provider Dialect
}

// Catalog is the immutable protocol authority compiled into an Environment.
// It owns no codec implementation and cannot select a route.
type Catalog struct {
	operationsByDialect map[Dialect][]ClientOperationDefinition
	codecPairs          map[codecPairKey]CodecPlan
}

func NewCatalog(
	definitions []ClientOperationDefinition,
	pairs []CodecPairDefinition,
) (Catalog, error) {
	if len(definitions) == 0 || len(pairs) == 0 {
		return Catalog{}, ErrInvalidSpecification
	}
	byID := make(map[ClientOperationID]ClientOperationDefinition, len(definitions))
	operationsByDialect := make(map[Dialect][]ClientOperationDefinition)
	for _, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return Catalog{}, err
		}
		if _, duplicate := byID[definition.ID()]; duplicate {
			return Catalog{}, fmt.Errorf("%w: duplicate operation %q", ErrInvalidSpecification, definition.ID().String())
		}
		byID[definition.ID()] = definition.Clone()
		operationsByDialect[definition.ClientDialect()] = append(
			operationsByDialect[definition.ClientDialect()],
			definition.Clone(),
		)
	}
	for dialect := range operationsByDialect {
		sort.Slice(operationsByDialect[dialect], func(left, right int) bool {
			return operationsByDialect[dialect][left].ID().String() <
				operationsByDialect[dialect][right].ID().String()
		})
	}

	codecPairs := make(map[codecPairKey]CodecPlan, len(pairs))
	seenPairIDs := make(map[CodecPairID]struct{}, len(pairs))
	for _, pair := range pairs {
		if pair.Revision == 0 || pair.Revision > MaxRevision ||
			!pair.ClientDialect.Valid() || !pair.ProviderDialect.Valid() ||
			len(pair.ClientOperationIDs) == 0 {
			return Catalog{}, ErrInvalidSpecification
		}
		if _, duplicate := seenPairIDs[pair.ID]; duplicate {
			return Catalog{}, fmt.Errorf("%w: duplicate codec pair ID %q", ErrInvalidSpecification, pair.ID.String())
		}
		seenPairIDs[pair.ID] = struct{}{}
		key := codecPairKey{client: pair.ClientDialect, provider: pair.ProviderDialect}
		if _, duplicate := codecPairs[key]; duplicate {
			return Catalog{}, fmt.Errorf("%w: duplicate codec pair", ErrInvalidSpecification)
		}
		operationIDs := slices.Clone(pair.ClientOperationIDs)
		seenOperations := make(map[ClientOperationID]struct{}, len(operationIDs))
		pairDefinitions := make([]ClientOperationDefinition, 0, len(operationIDs))
		for _, operationID := range operationIDs {
			definition, exists := byID[operationID]
			if !exists || definition.ClientDialect() != pair.ClientDialect {
				return Catalog{}, fmt.Errorf("%w: codec pair operation %q is incompatible", ErrInvalidSpecification, operationID.String())
			}
			if _, duplicate := seenOperations[operationID]; duplicate {
				return Catalog{}, fmt.Errorf("%w: codec pair operation is duplicated", ErrInvalidSpecification)
			}
			seenOperations[operationID] = struct{}{}
			pairDefinitions = append(pairDefinitions, definition)
		}
		required := slices.Clone(pair.RequiredCapabilities)
		sort.Slice(required, func(left, right int) bool { return required[left] < required[right] })
		for index, capability := range required {
			if !capability.Valid() || index > 0 && capability == required[index-1] {
				return Catalog{}, ErrInvalidSpecification
			}
		}
		plan, err := NewCodecPlan(
			pair.ID,
			pair.Revision,
			pair.ClientDialect,
			pair.ProviderDialect,
			pairDefinitions,
			required,
		)
		if err != nil {
			return Catalog{}, err
		}
		codecPairs[key] = plan
	}
	return Catalog{operationsByDialect: operationsByDialect, codecPairs: codecPairs}, nil
}

func (catalog Catalog) Resolve(client, provider Dialect) (CodecPlan, error) {
	if !client.Valid() || !provider.Valid() {
		return CodecPlan{}, ErrUnknownDialect
	}
	plan, exists := catalog.codecPairs[codecPairKey{client: client, provider: provider}]
	if !exists {
		return CodecPlan{}, fmt.Errorf(
			"%w: client=%q provider=%q",
			ErrUnsupportedCodecPair,
			client,
			provider,
		)
	}
	return cloneCodecPlan(plan), nil
}

func (catalog Catalog) OperationsForDialect(dialect Dialect) ([]ClientOperationDefinition, error) {
	if !dialect.Valid() {
		return nil, ErrUnknownDialect
	}
	operations := catalog.operationsByDialect[dialect]
	if len(operations) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrUnknownDialect, dialect)
	}
	return cloneOperationDefinitions(operations), nil
}

func cloneCodecPlan(plan CodecPlan) CodecPlan {
	cloned := plan
	cloned.clientOperations = cloneOperationDefinitions(plan.clientOperations)
	cloned.requiredCapabilities = slices.Clone(plan.requiredCapabilities)
	return cloned
}

func cloneOperationDefinitions(source []ClientOperationDefinition) []ClientOperationDefinition {
	result := make([]ClientOperationDefinition, len(source))
	for index, operation := range source {
		result[index] = operation.Clone()
	}
	return result
}
