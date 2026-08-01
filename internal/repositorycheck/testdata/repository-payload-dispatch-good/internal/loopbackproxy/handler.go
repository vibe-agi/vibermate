package loopbackproxy

// Separate arms keep the payload-bearing decision independent from opaque
// forwarding.
func dispatch(kind string) string {
	switch kind {
	case KindSemantic:
		return "semantic"
	case KindAuxiliary:
		return "probe"
	case KindOpaque:
		return "original"
	default:
		return "rejected"
	}
}

const (
	KindSemantic  = "semantic"
	KindAuxiliary = "auxiliary"
	KindOpaque    = "opaque"
)
