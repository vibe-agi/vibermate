package loopbackproxy

// Merging the arms is exactly the shape that sent payload-bearing auxiliary
// requests to the original origin.
func dispatch(kind string) string {
	switch kind {
	case KindSemantic:
		return "semantic"
	case KindAuxiliary, KindOpaque:
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
