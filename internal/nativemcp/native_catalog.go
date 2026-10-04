package nativemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// ContentProfiles is trusted composition for retention only. It cannot grant
// execution. Unknown profiles preserve metadata and suppress content hashing.
type ContentProfiles interface {
	Resolve(environment.ClientToolContract, nativetool.CatalogDefinition) (ContentProfile, bool)
}
type ContentProfile struct {
	contract          environment.ClientToolContract
	definition        nativetool.Digest
	arguments, result []string
}
type retentionState struct {
	profile            ContentProfile
	known, unavailable bool
}

func frozenProfile(p ContentProfile) ContentProfile {
	p.arguments = append([]string{}, p.arguments...)
	p.result = append([]string{}, p.result...)
	return p
}

func (p ContentProfile) valid(contract environment.ClientToolContract, d nativetool.CatalogDefinition) bool {
	if p.contract != contract || p.definition == (nativetool.Digest{}) || p.definition != d.Digest() {
		return false
	}
	for _, paths := range [][]string{p.arguments, p.result} {
		if len(paths) > 64 {
			return false
		}
		for _, path := range paths {
			if len(path) == 0 || len(path) > 512 || !strings.HasPrefix(path, "/") {
				return false
			}
		}
	}
	return true
}
func containsPath(value map[string]json.RawMessage, path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, part := range parts {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		raw, present := value[part]
		if !present {
			return false
		}
		if index == len(parts)-1 {
			return true
		}
		if json.Unmarshal(raw, &value) != nil {
			return false
		}
	}
	return false
}
func retainedContent(raw []byte, paths []string, l Limits) ([]byte, bool) {
	object, err := decodeObject(raw, l.ResultBytes, l.Depth)
	if err != nil {
		return nil, false
	}
	for _, path := range paths {
		if containsPath(object, path) {
			return nil, false
		}
	}
	if _, present := object["_meta"]; !present {
		return raw, true
	}
	delete(object, "_meta")
	clean, err := json.Marshal(object)
	return clean, err == nil
}
func (e *executor) contentProfile(s *session, name string) (ContentProfile, bool) {
	if e.dependencies.ContentProfiles == nil {
		return ContentProfile{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.catalog == nil {
		return ContentProfile{}, false
	}
	definition, ok := s.catalog.Lookup(name)
	if !ok {
		return ContentProfile{}, false
	}
	contract := s.plan.Route().Contract
	profile, ok := e.dependencies.ContentProfiles.Resolve(contract, definition)
	return profile, ok && profile.valid(contract, definition)
}

type catalogCandidate struct {
	evidence               nativetool.CatalogEvidence
	scope                  nativetool.CatalogScope
	collection             uint64
	temporary, publicBytes int64
	adopted                bool
}

func (e *executor) releaseCatalogCandidate(candidate *catalogCandidate) {
	if candidate == nil {
		return
	}
	e.budget.release(candidate.temporary)
	if !candidate.adopted {
		e.budget.release(candidate.publicBytes)
	}
}

// Validate the authenticated collection before recording its RPC terminal.
// This does not publish catalog evidence or establish current Store authority.
func (e *executor) prepareCatalog(r rpc, s *session) (*catalogCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.collection != s.collection {
		return nil, nil
	}
	if s.closed || len(s.pages) == 0 {
		return nil, errSession
	}
	last := s.pages[len(s.pages)-1]
	object, err := decodeObject(last.ExactResponse, e.limits.CatalogPageBytes, e.limits.Depth)
	if err != nil {
		return nil, err
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(object["result"], &result) != nil {
		return nil, errProtocol
	}
	if cursor, present := result["nextCursor"]; present {
		next, ok := text(cursor)
		if !ok || len(next) > e.limits.OpaqueIDBytes {
			return nil, errProtocol
		}
		if next != "" {
			if next == s.nextCursor {
				return nil, errProtocol
			}
			s.nextCursor = next
			return nil, nil
		}
	}
	scope := nativetool.CatalogScope{Capture: s.owner.capture, Plan: s.plan, AssignmentRevision: s.assignment, OwnerEpoch: s.owner.epoch, SessionID: s.id, SessionRevision: s.revision + 1, CatalogGeneration: s.generation + 1}
	observer, err := nativetool.NewCatalogObserver(scope)
	if err != nil {
		return nil, err
	}
	temporary := 3 * s.catalogBytes
	if !e.budget.acquire(temporary) {
		return nil, errBudget
	}
	prepared := false
	defer func() {
		if !prepared {
			e.budget.release(temporary)
		}
	}()
	evidence, err := observer.Observe(s.pages)
	if err != nil {
		return nil, err
	}
	if len(evidence.Definitions()) > e.limits.CatalogTools || !evidence.MatchesRequests(s.listDigests) {
		return nil, errProtocol
	}
	// The observer clones the public projection; account that live allocation
	// alongside retained wire pages until old page buffers are released.
	publicBytes := int64(0)
	for _, definition := range evidence.Definitions() {
		mapped := definition.Definition()
		publicBytes += int64(len(mapped.InputSchema.Bytes()) + len(mapped.Description) + len(mapped.Name) + len(definition.Annotations().Bytes()))
	}
	if !e.budget.acquire(publicBytes) {
		return nil, errBudget
	}
	prepared = true
	return &catalogCandidate{evidence: evidence, scope: scope, collection: r.collection, temporary: temporary, publicBytes: publicBytes}, nil
}

// Only the actual Store can publish an already validated complete collection.
func (e *executor) publishCatalog(ctx context.Context, candidate *catalogCandidate, s *session) error {
	if candidate == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if candidate.collection != s.collection {
		return nil
	}
	if s.closed || s.renewalFailed {
		return errSession
	}
	revision, err := e.dependencies.Store.SetCatalog(ctx, s.id, s.revision, candidate.evidence, e.dependencies.Clock().UTC())
	if err != nil {
		return err
	}
	if revision != candidate.scope.SessionRevision {
		return errSession
	}
	s.revision = revision
	s.generation = candidate.scope.CatalogGeneration
	s.catalog = &candidate.evidence
	s.nextCursor = ""
	publicBytes := candidate.publicBytes
	candidate.adopted = true
	s.retained += publicBytes
	s.retained -= s.publicBytes
	e.budget.release(s.publicBytes)
	s.publicBytes = publicBytes
	pageBytes := int64(0)
	for _, page := range s.pages {
		pageBytes += int64(len(page.ExactRequest) + len(page.ExactResponse))
	}
	s.pages = nil
	s.listDigests = nil
	s.catalogBytes = 0
	s.retained -= pageBytes
	e.budget.release(pageBytes)
	return nil
}
func (e *executor) observeCatalogPage(r rpc, s *session, b nativetool.Binding, response []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.collection != s.collection {
		return nil
	}
	size := int64(len(r.raw) + len(response))
	if int64(len(response)) > e.limits.CatalogPageBytes || s.catalogBytes+size > e.limits.CatalogBytes || len(s.pages) >= e.limits.CatalogPages {
		return errBudget
	}
	if !e.budget.acquire(size) {
		return errBudget
	}
	s.retained += size
	s.catalogBytes += size
	s.pages = append(s.pages, nativetool.CatalogPageObservation{ExactRequest: bytes.Clone(r.raw), ExactResponse: bytes.Clone(response)})
	s.listDigests = append(s.listDigests, b.RequestDigest())
	return nil
}

func (e *executor) discardCatalog(s *session) {
	s.collection++
	size := s.catalogBytes
	s.pages = nil
	s.listDigests = nil
	s.catalogBytes = 0
	s.nextCursor = ""
	s.retained -= size
	e.budget.release(size)
}
