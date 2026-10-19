package nativemcp

import (
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// Immutable fixed fallback is borrowed; optional preparation never allocates
// a per-call empty argument object before recording admission.
var emptyToolArguments = [2]byte{'{', '}'}

func (e *executor) observeContent(r rpc, part nativetool.ContentPart, raw []byte) {
	if r.retention == nil || !r.retention.known || !r.retention.stamp.Current() {
		return
	}
	if e.dependencies.ContentRecorder == nil {
		r.retention.unavailable = true
		return
	}
	result := e.dependencies.ContentRecorder.ObserveNativeContent(r.retention.stamp, ContentInput{identity: r.retention.identity, observedAt: e.dependencies.Clock().UTC(), part: part, raw: raw, profile: r.retention.profile, limits: e.limits})
	if result != recording.Accepted && result != recording.DroppedInvalid {
		r.retention.unavailable = true
	}
}
