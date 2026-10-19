package nativetool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
	"time"
)

// InspectionOperation contains optional retained observations, never authority.
// Unknown lifecycle has State="unknown" and Revision=0. Legacy observation
// times stay nil because the released format did not record them.
type InspectionOperation struct {
	Operation         Operation
	Arguments, Result *InspectionContentDescriptor
	Token             recording.InspectionToken
}
type InspectionContentDescriptor struct {
	ContentDescriptor
	ObservedAt *time.Time
	Reference  string
}
type InspectionPageRequest struct {
	Capture captureidentity.Reference
	Cursor  string
	Limit   int
}
type InspectionPage struct {
	Items      []InspectionOperation
	NextCursor string
	Token      recording.InspectionToken
}
type InspectionContentRequest struct {
	Token         recording.InspectionToken
	Capture       captureidentity.Reference
	OperationID   ID
	Part          ContentPart
	Descriptor    InspectionContentDescriptor
	Offset, Limit int
}
type InspectionReader interface {
	Get(context.Context, captureidentity.Reference, ID) (InspectionOperation, error)
	Page(context.Context, InspectionPageRequest) (InspectionPage, error)
	ReadContent(context.Context, InspectionContentRequest) ([]byte, error)
	Check(context.Context, recording.InspectionToken) error
}

// InspectionVersion identifies the visibility/source generation of a response.
func InspectionVersion(token recording.InspectionToken) string {
	b, _ := json.Marshal(token)
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}
