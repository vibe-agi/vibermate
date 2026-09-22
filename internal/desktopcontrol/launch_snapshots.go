package desktopcontrol

import (
	"github.com/vibe-agi/vibermate/internal/launchsnapshot"
	"net/http"
)

func (handler *Handler) listLaunchSnapshots(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	items := []launchsnapshot.Snapshot{}
	if handler.launchSnapshots != nil {
		items = handler.launchSnapshots.List()
	}
	writeJSON(writer, http.StatusOK, struct {
		Items []launchsnapshot.Snapshot `json:"items"`
	}{items})
}
