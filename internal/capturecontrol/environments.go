package capturecontrol

import (
	"net/http"
	"slices"
	"strings"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/environment"
)

const (
	EnvironmentsPath   = "/api/v1/capture-environments"
	EnvironmentsSchema = "vibermate-capture-environments/v1"
)

// CaptureEnvironment exposes launch choices, not management configuration,
// account identities, credentials, or child-process environment values.
type CaptureEnvironment struct {
	ID   environment.EnvironmentID `json:"id"`
	Name string                    `json:"name"`
}

type CaptureEnvironments struct {
	Schema string               `json:"schema"`
	Items  []CaptureEnvironment `json:"items"`
}

func (handler *Handler) listEnvironments(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Header.Get("Proxy-Authorization") != "" {
		writeProblem(writer, http.StatusForbidden, ReasonControlPrincipalUnauthorized)
		return
	}
	principal, authenticated := handler.authenticatePrincipal(request)
	if !authenticated {
		writeProblem(writer, http.StatusUnauthorized, ReasonControlPrincipalUnauthorized)
		return
	}
	if !principal.Allows(controlprincipal.GrantCaptureRun) ||
		(principal.Kind() != controlprincipal.KindLocalCLI && principal.Kind() != controlprincipal.KindRuntimeUser) {
		writeProblem(writer, http.StatusForbidden, ReasonCaptureGrantNotAllowed)
		return
	}
	if request.URL.RawQuery != "" || request.URL.ForceQuery || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidCaptureRun)
		return
	}
	if handler.environments == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonProjectionUnavailable)
		return
	}
	snapshots, err := handler.environments.List(request.Context())
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonProjectionUnavailable)
		return
	}
	page := CaptureEnvironments{Schema: EnvironmentsSchema, Items: []CaptureEnvironment{}}
	for _, snapshot := range snapshots {
		if snapshot.State() != environment.StateActive ||
			principal.Kind() == controlprincipal.KindRuntimeUser && !principal.AllowsEnvironment(snapshot.ID().String()) {
			continue
		}
		page.Items = append(page.Items, CaptureEnvironment{ID: snapshot.ID(), Name: snapshot.Name()})
	}
	slices.SortFunc(page.Items, func(left, right CaptureEnvironment) int {
		return strings.Compare(left.ID.String(), right.ID.String())
	})
	writeJSON(writer, http.StatusOK, page)
}
