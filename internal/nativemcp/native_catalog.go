package nativemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// ContentProfiles is trusted classification for sensitive binding, approval
// previews and optional retention. It cannot grant execution. Unknown profiles
// suppress optional content without manufacturing a trusted classification.
type ContentProfiles interface {
	Resolve(environment.ClientToolContract, nativetool.CatalogDefinition) (ContentProfile, bool)
}
type ContentProfile struct {
	contract          environment.ClientToolContract
	definition        nativetool.Digest
	arguments, result []string
}
type retentionState struct {
	identity           nativetool.ContentIdentity
	stamp              recording.Stamp
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
	var current json.RawMessage
	for index, part := range parts {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		var raw json.RawMessage
		if index == 0 {
			var present bool
			raw, present = value[part]
			if !present {
				return false
			}
		} else {
			current = bytes.TrimSpace(current)
			if len(current) == 0 {
				return false
			}
			switch current[0] {
			case '{':
				var object map[string]json.RawMessage
				if json.Unmarshal(current, &object) != nil {
					return false
				}
				var present bool
				raw, present = object[part]
				if !present {
					return false
				}
			case '[':
				elements, valid := contentElements(current)
				ordinal, err := strconv.Atoi(part)
				if !valid || err != nil || ordinal < 0 || ordinal >= len(elements) || strconv.Itoa(ordinal) != part {
					return false
				}
				raw = elements[ordinal]
			default:
				return false
			}
		}
		if index == len(parts)-1 {
			return true
		}
		current = raw
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
	// Arguments are classified business objects. A field named _meta is not
	// transport metadata here; only exact profile-sensitive paths suppress it.
	return raw, true
}

func retainedToolResult(raw []byte, paths []string, l Limits) ([]byte, bool) {
	if _, safe := retainedContent(raw, paths, l); !safe {
		return nil, false
	}
	object, err := decodeObject(raw, l.ResultBytes, l.Depth)
	if err != nil {
		return nil, false
	}
	if validateMethodResult(nativetool.RequestToolsCall, rpc{object: map[string]json.RawMessage{"result": raw}}, l) != nil {
		return nil, false
	}
	changed := false
	if _, ok := object["_meta"]; ok {
		delete(object, "_meta")
		changed = true
	}
	if elements, ok := contentElements(object["content"]); ok {
		for i, element := range elements {
			block, _ := decodeObject(element, l.ResultBytes, l.Depth)
			blockChanged := false
			if _, present := block["_meta"]; present {
				delete(block, "_meta")
				blockChanged = true
			}
			if kind, _ := text(block["type"]); kind == "resource" {
				resource, _ := decodeObject(block["resource"], l.ResultBytes, l.Depth)
				if _, present := resource["_meta"]; present {
					delete(resource, "_meta")
					block["resource"], err = json.Marshal(resource)
					if err != nil {
						return nil, false
					}
					blockChanged = true
				}
			}
			if blockChanged {
				elements[i], err = json.Marshal(block)
				if err != nil {
					return nil, false
				}
				changed = true
			}
		}
		if changed {
			object["content"], err = json.Marshal(elements)
			if err != nil {
				return nil, false
			}
		}
	}
	if !changed {
		return raw, true
	}
	clean, err := json.Marshal(object)
	return clean, err == nil
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
