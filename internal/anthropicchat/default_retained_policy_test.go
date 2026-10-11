package anthropicchat

import (
	"encoding/json"
	"testing"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

// Keep the private tool accumulator in its owning package: the retained charge
// includes the accumulator itself, the integer map key, and the pointer cell.
func TestDefaultChatCompleteRetainedStructure(t *testing.T) {
	e := uint64(ssewire.DefaultOptions().MaxEvents)
	k := uint64(protocolcore.MaxToolCount)
	extra := uint64(4 + protocolcore.MaxProviderExtensions + 2)
	tool := uint64(unsafe.Sizeof(streamToolAccumulator{})) + uint64(unsafe.Sizeof(int(0))) + uint64(unsafe.Sizeof((*streamToolAccumulator)(nil)))
	structure := e*uint64(unsafe.Sizeof(json.RawMessage{})) + 2*e*uint64(unsafe.Sizeof(protocolcore.TranslationNotice{})) + k*tool + uint64(unsafe.Sizeof(protocolcore.Response{})) + uint64(unsafe.Sizeof(protocolcore.ProviderExtension{})) + uint64(unsafe.Sizeof(json.RawMessage{})) + 2*extra*uint64(unsafe.Sizeof(protocolcore.TranslationNotice{}))
	if structure > protocolcore.DefaultResourceLimits().Response.StructureBytes {
		t.Fatalf("complete retained Chat structure %d exceeds default", structure)
	}
}
