package desktopcontrol

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountselector"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

type CodeLibraryCollectionResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type CodeLibraryTransformResponse struct {
	ID           string                  `json:"id"`
	Revision     uint64                  `json:"revision"`
	CollectionID string                  `json:"collectionId"`
	DisplayName  string                  `json:"displayName"`
	Policy       messagetransform.Policy `json:"policy"`
	PublishedAt  string                  `json:"publishedAt"`
}

type CodeLibraryAccountSelectorResponse struct {
	ID           string                 `json:"id"`
	Revision     uint64                 `json:"revision"`
	CollectionID string                 `json:"collectionId"`
	DisplayName  string                 `json:"displayName"`
	Policy       accountselector.Policy `json:"policy"`
	PublishedAt  string                 `json:"publishedAt"`
}

type CodeLibraryCatalogResponse struct {
	Collections      []CodeLibraryCollectionResponse      `json:"collections"`
	Transforms       []CodeLibraryTransformResponse       `json:"transforms"`
	AccountSelectors []CodeLibraryAccountSelectorResponse `json:"accountSelectors"`
}

type codeLibraryCollectionInput struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type codeLibraryTransformInput struct {
	CollectionID string                  `json:"collectionId"`
	DisplayName  string                  `json:"displayName"`
	Policy       messagetransform.Policy `json:"policy"`
}

type codeLibraryAccountSelectorInput struct {
	CollectionID string                 `json:"collectionId"`
	DisplayName  string                 `json:"displayName"`
	Policy       accountselector.Policy `json:"policy"`
}

func (handler *Handler) listCodeLibrary(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	catalog, err := handler.codeLibrary.List(request.Context())
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonCodeLibraryUnavailable)
		return
	}
	response := CodeLibraryCatalogResponse{
		Collections:      make([]CodeLibraryCollectionResponse, len(catalog.Collections)),
		Transforms:       make([]CodeLibraryTransformResponse, len(catalog.Transforms)),
		AccountSelectors: make([]CodeLibraryAccountSelectorResponse, len(catalog.AccountSelectors)),
	}
	for index, collection := range catalog.Collections {
		response.Collections[index] = codeLibraryCollectionResponseOf(collection)
	}
	for index, transform := range catalog.Transforms {
		response.Transforms[index] = codeLibraryTransformResponseOf(transform)
	}
	for index, selector := range catalog.AccountSelectors {
		response.AccountSelectors[index] = codeLibraryAccountSelectorResponseOf(selector)
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) createCodeLibraryCollection(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	var input codeLibraryCollectionInput
	if headerErr != nil || bodyErr != nil || expected != 0 ||
		decodeStrictJSON(body, &input) != nil || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, err := codelibrary.NewCollectionID(input.ID)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := codeLibraryFingerprint(request, expected, body)
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		collection, createErr := handler.codeLibrary.CreateCollection(request.Context(), codelibrary.CreateCollectionCommand{
			ID: id, DisplayName: input.DisplayName,
		})
		if createErr != nil {
			return problemResponse(classifyCodeLibraryError(createErr))
		}
		return jsonResponse(http.StatusCreated, codeLibraryCollectionResponseOf(collection))
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonCodeLibraryConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) publishCodeLibraryTransform(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	var input codeLibraryTransformInput
	decodeErr := bodyErr
	if decodeErr == nil {
		decodeErr = decodeStrictJSON(body, &input)
	}
	transformID, transformErr := codelibrary.NewTransformID(request.PathValue("transformId"))
	collectionID, collectionErr := codelibrary.NewCollectionID(input.CollectionID)
	if headerErr != nil || decodeErr != nil || transformErr != nil || collectionErr != nil ||
		request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := codeLibraryFingerprint(request, expected, body)
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		transform, publishErr := handler.codeLibrary.PublishTransform(request.Context(), codelibrary.PublishTransformCommand{
			ID: transformID, ExpectedRevision: codelibrary.Revision(expected),
			CollectionID: collectionID, DisplayName: input.DisplayName, Policy: input.Policy,
		})
		if publishErr != nil {
			return problemResponse(classifyCodeLibraryError(publishErr))
		}
		return jsonResponse(http.StatusOK, codeLibraryTransformResponseOf(transform))
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonCodeLibraryConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) getCodeLibraryTransformRevision(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, idErr := codelibrary.NewTransformID(request.PathValue("transformId"))
	revision, revisionErr := strconv.ParseUint(request.PathValue("transformRevision"), 10, 64)
	if idErr != nil || revisionErr != nil || revision == 0 || revision > uint64(codelibrary.MaxRevision) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	transform, err := handler.codeLibrary.GetTransformRevision(
		request.Context(), id, codelibrary.Revision(revision),
	)
	if err != nil {
		spec := classifyCodeLibraryError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	writeJSON(writer, http.StatusOK, codeLibraryTransformResponseOf(transform))
}

func (handler *Handler) publishCodeLibraryAccountSelector(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	var input codeLibraryAccountSelectorInput
	decodeErr := bodyErr
	if decodeErr == nil {
		decodeErr = decodeStrictJSON(body, &input)
	}
	selectorID, selectorErr := codelibrary.NewAccountSelectorID(request.PathValue("selectorId"))
	collectionID, collectionErr := codelibrary.NewCollectionID(input.CollectionID)
	if headerErr != nil || decodeErr != nil || selectorErr != nil || collectionErr != nil ||
		request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := codeLibraryFingerprint(request, expected, body)
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		selector, publishErr := handler.codeLibrary.PublishAccountSelector(
			request.Context(),
			codelibrary.PublishAccountSelectorCommand{
				ID: selectorID, ExpectedRevision: codelibrary.Revision(expected),
				CollectionID: collectionID, DisplayName: input.DisplayName, Policy: input.Policy,
			},
		)
		if publishErr != nil {
			return problemResponse(classifyCodeLibraryError(publishErr))
		}
		return jsonResponse(http.StatusOK, codeLibraryAccountSelectorResponseOf(selector))
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonCodeLibraryConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) getCodeLibraryAccountSelectorRevision(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, idErr := codelibrary.NewAccountSelectorID(request.PathValue("selectorId"))
	revision, revisionErr := strconv.ParseUint(request.PathValue("selectorRevision"), 10, 64)
	if idErr != nil || revisionErr != nil || revision == 0 || revision > uint64(codelibrary.MaxRevision) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	selector, err := handler.codeLibrary.GetAccountSelectorRevision(
		request.Context(), id, codelibrary.Revision(revision),
	)
	if err != nil {
		spec := classifyCodeLibraryError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	writeJSON(writer, http.StatusOK, codeLibraryAccountSelectorResponseOf(selector))
}

func codeLibraryFingerprint(request *http.Request, expected uint64, body []byte) [sha256.Size]byte {
	return sha256.Sum256(bytes.Join([][]byte{
		[]byte(request.Method), []byte(request.URL.Path), []byte(strconv.FormatUint(expected, 10)), body,
	}, []byte{0}))
}

func codeLibraryCollectionResponseOf(collection codelibrary.Collection) CodeLibraryCollectionResponse {
	return CodeLibraryCollectionResponse{ID: collection.ID.String(), DisplayName: collection.DisplayName}
}

func codeLibraryTransformResponseOf(transform codelibrary.TransformRevision) CodeLibraryTransformResponse {
	return CodeLibraryTransformResponse{
		ID: transform.ID.String(), Revision: uint64(transform.Revision),
		CollectionID: transform.CollectionID.String(), DisplayName: transform.DisplayName,
		Policy: transform.Policy, PublishedAt: transform.PublishedAt.Format(time.RFC3339Nano),
	}
}

func codeLibraryAccountSelectorResponseOf(
	selector codelibrary.AccountSelectorRevision,
) CodeLibraryAccountSelectorResponse {
	return CodeLibraryAccountSelectorResponse{
		ID: selector.ID.String(), Revision: uint64(selector.Revision),
		CollectionID: selector.CollectionID.String(), DisplayName: selector.DisplayName,
		Policy: selector.Policy, PublishedAt: selector.PublishedAt.Format(time.RFC3339Nano),
	}
}

func classifyCodeLibraryError(err error) problemSpec {
	switch {
	case errors.Is(err, codelibrary.ErrInvalidLibrary):
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest}
	case errors.Is(err, codelibrary.ErrCollectionNotFound), errors.Is(err, codelibrary.ErrTransformNotFound),
		errors.Is(err, codelibrary.ErrSelectorNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonCodeLibraryNotFound}
	case errors.Is(err, codelibrary.ErrRevisionConflict):
		return problemSpec{status: http.StatusConflict, reason: ReasonCodeLibraryConflict}
	default:
		return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonCodeLibraryUnavailable}
	}
}
