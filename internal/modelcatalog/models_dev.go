package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultModelsDevURL = "https://models.dev/models.json"
	defaultMetadataTTL  = 6 * time.Hour
)

type ModelsDevOptions struct {
	Transport MetadataTransport
	Clock     Clock
	CacheTTL  time.Duration
}

// ModelsDev is an optional descriptive directory. It never decides whether a
// model is available on an Endpoint and it is not consulted on the request
// path after its in-memory snapshot has been populated.
type ModelsDev struct {
	transport MetadataTransport
	clock     Clock
	cacheTTL  time.Duration

	loadMu sync.Mutex
	mu     sync.Mutex
	cache  *modelsDevSnapshot
}

type modelsDevSnapshot struct {
	expiresAt  time.Time
	exact      map[string]Metadata
	byProvider map[string][]Metadata
}

type modelsDevEntry struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Description      string              `json:"description"`
	Family           string              `json:"family"`
	Attachment       bool                `json:"attachment"`
	Reasoning        bool                `json:"reasoning"`
	ToolCall         bool                `json:"tool_call"`
	StructuredOutput bool                `json:"structured_output"`
	OpenWeights      bool                `json:"open_weights"`
	Knowledge        string              `json:"knowledge"`
	ReleaseDate      string              `json:"release_date"`
	Modalities       modelsDevModalities `json:"modalities"`
	Limit            modelsDevLimit      `json:"limit"`
}

type modelsDevModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type modelsDevLimit struct {
	Context int64 `json:"context"`
	Output  int64 `json:"output"`
}

func NewModelsDev(options ModelsDevOptions) (*ModelsDev, error) {
	if options.Transport == nil || options.Clock == nil {
		return nil, errors.New("models.dev dependencies are incomplete")
	}
	cacheTTL := options.CacheTTL
	if cacheTTL == 0 {
		cacheTTL = defaultMetadataTTL
	}
	if cacheTTL < 0 {
		return nil, errors.New("models.dev cache TTL is invalid")
	}
	return &ModelsDev{
		transport: options.Transport,
		clock:     options.Clock,
		cacheTTL:  cacheTTL,
	}, nil
}

func (directory *ModelsDev) Lookup(ctx context.Context, modelID string) (Metadata, bool, error) {
	if directory == nil || ctx == nil || !validMetadataID(modelID) {
		return Metadata{}, false, ErrInvalidCatalog
	}
	snapshot, err := directory.snapshot(ctx)
	if err != nil {
		return Metadata{}, false, err
	}
	// Endpoint model IDs are opaque provider-owned values. Metadata may be
	// attached only when that exact value is also a models.dev identity; ':',
	// '_', '/', and every other byte have no ViberMate-defined semantics.
	metadata, found := snapshot.exact[modelID]
	if !found {
		return Metadata{}, false, nil
	}
	return metadata.clone(), true, nil
}

func (directory *ModelsDev) ListProvider(ctx context.Context, provider string) ([]Metadata, error) {
	if directory == nil || ctx == nil || !validProviderID(provider) {
		return nil, ErrInvalidCatalog
	}
	snapshot, err := directory.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	items := snapshot.byProvider[provider]
	result := make([]Metadata, len(items))
	for index, item := range items {
		result[index] = item.clone()
	}
	return result, nil
}

func (directory *ModelsDev) snapshot(ctx context.Context) (*modelsDevSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := directory.clock.Now().UTC()
	directory.mu.Lock()
	cached := directory.cache
	directory.mu.Unlock()
	if cached != nil && now.Before(cached.expiresAt) {
		return cached, nil
	}

	directory.loadMu.Lock()
	defer directory.loadMu.Unlock()
	now = directory.clock.Now().UTC()
	directory.mu.Lock()
	cached = directory.cache
	directory.mu.Unlock()
	if cached != nil && now.Before(cached.expiresAt) {
		return cached, nil
	}

	loaded, err := directory.load(ctx, now)
	if err != nil {
		return nil, err
	}
	directory.mu.Lock()
	directory.cache = loaded
	directory.mu.Unlock()
	return loaded, nil
}

func (directory *ModelsDev) load(ctx context.Context, now time.Time) (*modelsDevSnapshot, error) {
	response, err := directory.transport.FetchModelsDev(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCatalogUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return nil, fmt.Errorf("%w: models.dev returned HTTP %d", ErrCatalogUnavailable, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxMetadataBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > MaxMetadataBodyBytes {
		return nil, ErrInvalidCatalog
	}
	wire := make(map[string]modelsDevEntry)
	if err := json.Unmarshal(body, &wire); err != nil || len(wire) == 0 || len(wire) > MaxCatalogModels {
		return nil, ErrInvalidCatalog
	}

	snapshot := &modelsDevSnapshot{
		expiresAt:  now.Add(directory.cacheTTL),
		exact:      make(map[string]Metadata),
		byProvider: make(map[string][]Metadata),
	}
	for canonicalID, entry := range wire {
		if entry.ID == "" {
			entry.ID = canonicalID
		}
		providerID, modelID, found := strings.Cut(canonicalID, "/")
		if !found || canonicalID != entry.ID || !validMetadataID(canonicalID) ||
			!validProviderID(providerID) || !validModelID(modelID) {
			continue
		}
		metadata := Metadata{
			CanonicalID:      canonicalID,
			DisplayName:      entry.Name,
			Description:      entry.Description,
			Family:           entry.Family,
			Reasoning:        entry.Reasoning,
			ToolCalls:        entry.ToolCall,
			StructuredOutput: entry.StructuredOutput,
			Attachments:      entry.Attachment,
			OpenWeights:      entry.OpenWeights,
			ContextLimit:     entry.Limit.Context,
			OutputLimit:      entry.Limit.Output,
			InputModalities:  append([]string(nil), entry.Modalities.Input...),
			OutputModalities: append([]string(nil), entry.Modalities.Output...),
			KnowledgeCutoff:  entry.Knowledge,
			ReleaseDate:      entry.ReleaseDate,
		}
		if !validMetadata(metadata) ||
			(metadata.KnowledgeCutoff != "" && !validModelName(metadata.KnowledgeCutoff)) ||
			(metadata.ReleaseDate != "" && !validModelName(metadata.ReleaseDate)) {
			continue
		}
		snapshot.exact[canonicalID] = metadata
		snapshot.byProvider[providerID] = append(snapshot.byProvider[providerID], metadata)
	}
	if len(snapshot.exact) == 0 {
		return nil, ErrInvalidCatalog
	}
	for provider := range snapshot.byProvider {
		sort.Slice(snapshot.byProvider[provider], func(left, right int) bool {
			return snapshot.byProvider[provider][left].CanonicalID < snapshot.byProvider[provider][right].CanonicalID
		})
	}
	return snapshot, nil
}
