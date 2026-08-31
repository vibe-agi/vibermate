package desktopcontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

type CaptureListResponse struct {
	Items      []CaptureResponse `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

const (
	captureListDefaultLimit = 50
	captureListMaximumLimit = 199
	captureCursorVersion    = 1
	maximumCaptureCursor    = 512
)

type captureCursorDocument struct {
	Version             int                  `json:"v"`
	Running             bool                 `json:"running"`
	UpdatedAtUnixMillis int64                `json:"updatedAtUnixMillis"`
	Kind                captureidentity.Kind `json:"kind"`
	ID                  string               `json:"id"`
}

type CaptureResponse struct {
	Key           string                 `json:"key"`
	ID            string                 `json:"id"`
	Kind          captureidentity.Kind   `json:"kind"`
	DisplayName   string                 `json:"displayName"`
	State         string                 `json:"state"`
	Observation   string                 `json:"observation"`
	CreatedAt     time.Time              `json:"createdAt"`
	UpdatedAt     time.Time              `json:"updatedAt"`
	ManagedRun    *ManagedRunResponse    `json:"managedRun,omitempty"`
	ManualCapture *ManualCaptureResponse `json:"manualCapture,omitempty"`
}

type ManagedRunResponse struct {
	ExecutableLabel             string                            `json:"executableLabel"`
	CWD                         string                            `json:"cwd"`
	CanonicalExecutablePath     string                            `json:"canonicalExecutablePath"`
	LocalUserLabel              string                            `json:"localUserLabel,omitempty"`
	MachineID                   string                            `json:"machineId,omitempty"`
	MachineRegistrationRevision uint64                            `json:"machineRegistrationRevision,omitempty"`
	WorkspaceID                 string                            `json:"workspaceId,omitempty"`
	WorkspaceLabel              string                            `json:"workspaceLabel,omitempty"`
	WorkspaceEvidence           string                            `json:"workspaceEvidence,omitempty"`
	WorkspaceDerivationRevision uint64                            `json:"workspaceDerivationRevision,omitempty"`
	ProcessID                   int                               `json:"processId,omitempty"`
	Recognition                 string                            `json:"recognition"`
	ClientAdapter               *capturecontrol.ClientAdapterView `json:"clientAdapter,omitempty"`
	ExpiresAt                   time.Time                         `json:"expiresAt"`
	FirstObservedAt             *time.Time                        `json:"firstObservedAt,omitempty"`
}

type ManualCaptureResponse struct {
	ClientClass        manualcapture.ClientClass        `json:"clientClass"`
	Lifetime           manualcapture.Lifetime           `json:"lifetime"`
	CredentialRevision manualcapture.CredentialRevision `json:"credentialRevision"`
	ExpiresAt          *time.Time                       `json:"expiresAt,omitempty"`
	LastObservedAt     *time.Time                       `json:"lastObservedAt,omitempty"`
}

type CaptureAssignmentResponse struct {
	CaptureKey                string                     `json:"captureKey"`
	CaptureID                 string                     `json:"captureId"`
	CaptureKind               captureidentity.Kind       `json:"captureKind"`
	EnvironmentID             environment.EnvironmentID  `json:"environmentId"`
	EnvironmentRevision       environment.Revision       `json:"environmentRevision"`
	EnvironmentDigest         string                     `json:"environmentDigest"`
	LaunchEnvironmentRevision environment.Revision       `json:"launchEnvironmentRevision"`
	LaunchEnvironmentDigest   string                     `json:"launchEnvironmentDigest"`
	Revision                  captureassignment.Revision `json:"revision"`
	Source                    captureassignment.Source   `json:"source"`
	UpdatedAt                 time.Time                  `json:"updatedAt"`
}

func captureRunResponseOf(view capturerun.View) CaptureResponse {
	reference, _ := captureidentity.New(captureidentity.KindManagedRun, view.ID)
	adapter := capturecontrol.CaptureRunViewOf(view).ClientAdapter
	var firstObserved *time.Time
	if !view.FirstObservedAt.IsZero() {
		value := view.FirstObservedAt
		firstObserved = &value
	}
	return CaptureResponse{
		Key: reference.Key(), ID: view.ID, Kind: reference.Kind,
		DisplayName: view.ExecutableLabel, State: string(view.State),
		Observation: string(view.Observation), CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt,
		ManagedRun: &ManagedRunResponse{
			ExecutableLabel: view.ExecutableLabel, CWD: view.CWD,
			CanonicalExecutablePath: view.CanonicalExecutablePath,
			LocalUserLabel:          view.LocalUserLabel, MachineID: view.MachineID,
			MachineRegistrationRevision: view.MachineRegistrationRevision,
			WorkspaceID:                 view.WorkspaceID, WorkspaceLabel: view.WorkspaceLabel,
			WorkspaceEvidence:           string(view.WorkspaceEvidence),
			WorkspaceDerivationRevision: view.WorkspaceDerivationRevision,
			ProcessID:                   view.ProcessID, Recognition: string(capturerun.NormalizedRecognition(view.Recognition)),
			ClientAdapter: adapter, ExpiresAt: view.ExpiresAt, FirstObservedAt: firstObserved,
		},
	}
}

func manualCaptureResponseOf(view manualcapture.View) CaptureResponse {
	reference, _ := captureidentity.New(captureidentity.KindManualCapture, view.ID)
	return CaptureResponse{
		Key: reference.Key(), ID: view.ID, Kind: reference.Kind,
		DisplayName: view.DisplayName, State: string(view.State), Observation: string(view.Observation),
		CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt,
		ManualCapture: &ManualCaptureResponse{
			ClientClass: view.ClientClass, Lifetime: view.Lifetime,
			CredentialRevision: view.CredentialRevision, ExpiresAt: view.ExpiresAt,
			LastObservedAt: view.LastObservedAt,
		},
	}
}

func assignmentResponseOf(assignment captureassignment.Assignment) CaptureAssignmentResponse {
	return CaptureAssignmentResponse{
		CaptureKey: assignment.Capture.Key(), CaptureID: assignment.Capture.ID,
		CaptureKind: assignment.Capture.Kind, EnvironmentID: assignment.EnvironmentID,
		EnvironmentRevision:       assignment.EnvironmentRevision,
		EnvironmentDigest:         assignment.EnvironmentDigest.String(),
		LaunchEnvironmentRevision: assignment.LaunchAuthority.InitialEnvironmentRevision(),
		LaunchEnvironmentDigest:   assignment.LaunchAuthority.InitialEnvironmentDigest().String(),
		Revision:                  assignment.Revision, Source: assignment.Source, UpdatedAt: assignment.UpdatedAt,
	}
}

func (handler *Handler) listCaptures(writer http.ResponseWriter, request *http.Request) {
	if handler.captureRuns == nil || handler.manualCaptures == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonCaptureUnavailable)
		return
	}
	for name, entries := range request.URL.Query() {
		if (name != "limit" && name != "cursor") || len(entries) != 1 {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
	}
	limit, err := captureListLimit(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	cursor, err := decodeCaptureCursor(request.URL.Query().Get("cursor"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	sourceLimit := limit + 1
	managedRequest := capturerun.PageRequest{Limit: sourceLimit}
	manualRequest := manualcapture.PageRequest{
		Owner: manualcapture.NewLocalOwnerScope(),
		Limit: sourceLimit,
	}
	if cursor != nil {
		managedRequest.Cursor = captureRunCursor(*cursor)
		manualRequest.Cursor = manualCaptureCursor(*cursor)
	}
	managed, err := handler.captureRuns.ListRuns(request.Context(), managedRequest)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonCaptureUnavailable)
		return
	}
	manual, err := handler.manualCaptures.List(request.Context(), manualRequest)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonCaptureUnavailable)
		return
	}
	items := make([]CaptureResponse, 0, len(managed.Items)+len(manual.Items))
	for _, view := range managed.Items {
		items = append(items, captureRunResponseOf(view))
	}
	for _, view := range manual.Items {
		items = append(items, manualCaptureResponseOf(view))
	}
	sort.Slice(items, func(left, right int) bool {
		return captureResponseLess(items[left], items[right])
	})
	response := CaptureListResponse{Items: items}
	if len(items) > limit {
		response.Items = items[:limit]
		response.NextCursor, err = encodeCaptureCursor(response.Items[limit-1])
		if err != nil {
			writeProblem(writer, http.StatusInternalServerError, ReasonRuntimeUnavailable)
			return
		}
	}
	writeJSON(writer, http.StatusOK, response)
}

func captureListLimit(request *http.Request) (int, error) {
	raw := request.URL.Query().Get("limit")
	if raw == "" {
		return captureListDefaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > captureListMaximumLimit {
		return 0, errors.New("Capture page limit is invalid")
	}
	return limit, nil
}

func captureKindOrder(kind captureidentity.Kind) int {
	if kind == captureidentity.KindManagedRun {
		return 0
	}
	return 1
}

func captureResponseRunning(response CaptureResponse) bool {
	if response.Kind == captureidentity.KindManagedRun {
		return response.State == string(capturerun.StateCreated) ||
			response.State == string(capturerun.StateAttached)
	}
	return response.State == string(manualcapture.StateActive)
}

func captureResponseLess(left, right CaptureResponse) bool {
	leftRunning := captureResponseRunning(left)
	rightRunning := captureResponseRunning(right)
	if leftRunning != rightRunning {
		return leftRunning
	}
	if !left.UpdatedAt.Equal(right.UpdatedAt) {
		return left.UpdatedAt.After(right.UpdatedAt)
	}
	leftKind := captureKindOrder(left.Kind)
	rightKind := captureKindOrder(right.Kind)
	if leftKind != rightKind {
		return leftKind < rightKind
	}
	return left.ID < right.ID
}

func encodeCaptureCursor(response CaptureResponse) (string, error) {
	document := captureCursorDocument{
		Version:             captureCursorVersion,
		Running:             captureResponseRunning(response),
		UpdatedAtUnixMillis: response.UpdatedAt.UnixMilli(),
		Kind:                response.Kind,
		ID:                  response.ID,
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeCaptureCursor(raw string) (*captureCursorDocument, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maximumCaptureCursor {
		return nil, errors.New("Capture cursor is too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != raw {
		return nil, errors.New("Capture cursor encoding is invalid")
	}
	var document captureCursorDocument
	if json.Unmarshal(payload, &document) != nil {
		return nil, errors.New("Capture cursor document is invalid")
	}
	canonical, err := json.Marshal(document)
	if err != nil || !bytes.Equal(canonical, payload) {
		return nil, errors.New("Capture cursor shape is invalid")
	}
	reference, err := captureidentity.New(document.Kind, document.ID)
	if err != nil || document.Version != captureCursorVersion ||
		document.UpdatedAtUnixMillis <= 0 || reference.ID != document.ID {
		return nil, errors.New("Capture cursor authority is invalid")
	}
	return &document, nil
}

func captureRunCursor(cursor captureCursorDocument) *capturerun.PageCursor {
	includeAtUpdatedAt := captureKindOrder(captureidentity.KindManagedRun) >=
		captureKindOrder(cursor.Kind)
	afterID := ""
	if cursor.Kind == captureidentity.KindManagedRun {
		afterID = cursor.ID
	}
	return &capturerun.PageCursor{
		Running:            cursor.Running,
		UpdatedAt:          time.UnixMilli(cursor.UpdatedAtUnixMillis).UTC(),
		AfterID:            afterID,
		IncludeAtUpdatedAt: includeAtUpdatedAt,
	}
}

func manualCaptureCursor(cursor captureCursorDocument) *manualcapture.PageCursor {
	includeAtUpdatedAt := captureKindOrder(captureidentity.KindManualCapture) >=
		captureKindOrder(cursor.Kind)
	afterID := ""
	if cursor.Kind == captureidentity.KindManualCapture {
		afterID = cursor.ID
	}
	return &manualcapture.PageCursor{
		Running:            cursor.Running,
		UpdatedAt:          time.UnixMilli(cursor.UpdatedAtUnixMillis).UTC(),
		AfterID:            afterID,
		IncludeAtUpdatedAt: includeAtUpdatedAt,
	}
}

func (handler *Handler) getCapture(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	reference, err := captureidentity.ParseKey(request.PathValue("captureKey"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	switch reference.Kind {
	case captureidentity.KindManagedRun:
		if handler.captureRuns == nil {
			writeProblem(writer, http.StatusServiceUnavailable, ReasonCaptureUnavailable)
			return
		}
		view, getErr := handler.captureRuns.GetRun(request.Context(), reference.ID)
		if getErr != nil {
			writeCaptureReadError(writer, getErr)
			return
		}
		writeJSON(writer, http.StatusOK, captureRunResponseOf(view))
	case captureidentity.KindManualCapture:
		if handler.manualCaptures == nil {
			writeProblem(writer, http.StatusServiceUnavailable, ReasonCaptureUnavailable)
			return
		}
		id, parseErr := manualcapture.ParseID(reference.ID)
		if parseErr != nil {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
		view, getErr := handler.manualCaptures.Get(request.Context(), manualcapture.NewLocalOwnerScope(), id)
		if getErr != nil {
			writeCaptureReadError(writer, getErr)
			return
		}
		writeJSON(writer, http.StatusOK, manualCaptureResponseOf(view))
	}
}

func (handler *Handler) getCaptureEnvironmentAssignment(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	reference, err := captureidentity.ParseKey(request.PathValue("captureKey"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	assignment, err := handler.assignments.Resolve(request.Context(), reference)
	if err != nil {
		spec := classifyAssignmentError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	writer.Header().Set("ETag", strconv.Quote("revision-"+strconv.FormatUint(uint64(assignment.Revision), 10)))
	writeJSON(writer, http.StatusOK, assignmentResponseOf(assignment))
}

func (handler *Handler) applyLatestCaptureEnvironment(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	reference, referenceErr := captureidentity.ParseKey(request.PathValue("captureKey"))
	if headerErr != nil || referenceErr != nil || expected == 0 ||
		expected >= uint64(captureassignment.MaxRevision) || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256(bytes.Join([][]byte{
		[]byte(request.Method), []byte(request.URL.Path), []byte(strconv.FormatUint(expected, 10)),
	}, []byte{0}))
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		assignment, applyErr := handler.assignments.ApplyLatest(
			request.Context(), reference, captureassignment.Revision(expected),
		)
		if applyErr != nil {
			return problemResponse(classifyAssignmentError(applyErr))
		}
		return jsonResponse(http.StatusOK, assignmentResponseOf(assignment))
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}

func writeCaptureReadError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, capturerun.ErrNotFound), errors.Is(err, manualcapture.ErrNotFound):
		writeProblem(writer, http.StatusNotFound, ReasonCaptureNotFound)
	case errors.Is(err, capturerun.ErrInvalidRequest), errors.Is(err, manualcapture.ErrInvalidCommand):
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
	default:
		writeProblem(writer, http.StatusServiceUnavailable, ReasonCaptureUnavailable)
	}
}

func classifyAssignmentError(err error) problemSpec {
	switch {
	case errors.Is(err, captureassignment.ErrAssignmentNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonCaptureAssignmentNotFound}
	case errors.Is(err, captureassignment.ErrInvalidAssignment):
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest}
	case errors.Is(err, captureassignment.ErrAssignmentConflict):
		return problemSpec{status: http.StatusConflict, reason: ReasonRevisionConflict}
	case errors.Is(err, captureassignment.ErrAssignmentIncompatible):
		return problemSpec{status: http.StatusConflict, reason: ReasonEnvironmentUnavailable}
	case errors.Is(err, environment.ErrEnvironmentNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonEnvironmentNotFound}
	case errors.Is(err, environment.ErrEnvironmentDisabled):
		return problemSpec{status: http.StatusConflict, reason: ReasonEnvironmentUnavailable}
	default:
		return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonCaptureUnavailable}
	}
}
