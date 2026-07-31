package protocolpath

import "github.com/vibe-agi/vibermate/internal/protocolcore"

// StreamStart carries only provider-validated identity needed to begin a
// client stream. The requested/effective model mapping remains frozen in the
// request owned by the encoder.
type StreamStart struct {
	ResponseID    string
	CreatedAtUnix int64
	ReportedModel string
}

// ClientStreamEncoder is one explicitly assembled client wire edge. The
// backend decoder invokes it only after the corresponding semantic transition
// has been validated. Implementations must reject invalid ordering and own
// their returned bytes.
type ClientStreamEncoder interface {
	Start(StreamStart) ([]byte, error)
	StartText(int) ([]byte, error)
	AppendText(int, string) ([]byte, error)
	StopText(int) ([]byte, error)
	ToolCall(int, protocolcore.ToolCall) ([]byte, error)
	Terminal(protocolcore.Response) ([]byte, error)
	TranslationReport() protocolcore.TranslationReport
}
