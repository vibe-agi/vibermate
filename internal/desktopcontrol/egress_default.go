package desktopcontrol

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
)

type egressDefaultResponse struct {
	Revision  uint64                 `json:"revision"`
	Profile   *EgressProfileResponse `json:"profile"`
	Effective struct {
		Profile         EgressProfileResponse         `json:"profile"`
		Source          egressprofile.SelectionSource `json:"source"`
		DefaultRevision uint64                        `json:"defaultRevision"`
	} `json:"effective"`
}

func defaultResponse(setting egressprofile.DefaultSetting) (egressDefaultResponse, error) {
	r := egressDefaultResponse{Revision: setting.Revision}
	p, source := egressprofile.Direct(), egressprofile.SelectionBuiltin
	if setting.Profile != nil {
		p = *setting.Profile
		source = egressprofile.SelectionGlobal
		view := egressProfileResponseOf(p)
		r.Profile = &view
	}
	if _, err := egressprofile.RestoreResolved(p, source, setting.Revision); err != nil {
		return r, err
	}
	r.Effective.Profile = egressProfileResponseOf(p)
	r.Effective.Source = source
	r.Effective.DefaultRevision = setting.Revision
	return r, nil
}
func (h *Handler) getEgressDefault(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeProblem(w, 422, ReasonInvalidRequest)
		return
	}
	if h.egressDefaults == nil {
		writeProblem(w, 503, ReasonEgressProfileUnavailable)
		return
	}
	setting, err := h.egressDefaults.GetDefault(r.Context())
	if err != nil {
		writeProblem(w, 503, ReasonEgressProfileUnavailable)
		return
	}
	view, err := defaultResponse(setting)
	if err != nil {
		writeProblem(w, 503, ReasonEgressProfileUnavailable)
		return
	}
	writeJSON(w, 200, view)
}

// Parse this closed mutation shape without duplicate/case-insensitive field matching.
func defaultObject(body []byte, names ...string) map[string]json.RawMessage {
	d := json.NewDecoder(bytes.NewReader(body))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok {
			return nil
		}
		known := false
		for _, n := range names {
			if n == name {
				known = true
				break
			}
		}
		if _, duplicate := fields[name]; duplicate || !known {
			return nil
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil
		}
		fields[name] = raw
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || d.Decode(new(any)) != io.EOF || len(fields) != len(names) {
		return nil
	}
	return fields
}
func (h *Handler) setEgressDefault(w http.ResponseWriter, r *http.Request) {
	expected, key, err := mutationHeaders(r)
	body, bodyErr := readJSONBody(r)
	fields := defaultObject(body, "profile")
	if err != nil || bodyErr != nil || fields == nil || expected == 0 || expected >= uint64(egressprofile.MaxRevision) || r.URL.RawQuery != "" {
		writeProblem(w, 422, ReasonInvalidRequest)
		return
	}
	var ref *egressprofile.ProfileRef
	if !bytes.Equal(bytes.TrimSpace(fields["profile"]), []byte("null")) {
		selected := defaultObject(fields["profile"], "id", "revision")
		var id string
		var rev uint64
		if selected == nil || json.Unmarshal(selected["id"], &id) != nil || json.Unmarshal(selected["revision"], &rev) != nil || rev == 0 || rev > uint64(egressprofile.MaxRevision) {
			writeProblem(w, 422, ReasonInvalidRequest)
			return
		}
		parsed, err := egressprofile.NewID(id)
		if err != nil {
			writeProblem(w, 422, ReasonInvalidRequest)
			return
		}
		ref = &egressprofile.ProfileRef{ID: parsed, Revision: egressprofile.Revision(rev)}
	}
	if h.egressDefaults == nil {
		writeProblem(w, 503, ReasonEgressProfileUnavailable)
		return
	}
	response, err := h.idempotent.execute(r.Context(), key, codeLibraryFingerprint(r, expected, body), func() cachedResponse {
		setting, err := h.egressDefaults.SetDefault(r.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: expected, Profile: ref})
		if err != nil {
			return problemResponse(classifyEgressProfileError(err))
		}
		view, err := defaultResponse(setting)
		if err != nil {
			return problemResponse(problemSpec{status: 503, reason: ReasonEgressProfileUnavailable})
		}
		return jsonResponse(200, view)
	})
	if err != nil {
		writeProblem(w, 409, ReasonEgressProfileConflict)
		return
	}
	writeCached(w, response)
}
