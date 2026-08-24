# Keep Client Session identity independent from Capture

Status: accepted

A Client Session is keyed by the native client kind and its opaque session identifier, with protocol-native conversation or actor identifiers distinguishing streams inside it. It may span multiple Captures, while a Capture remains the immutable boundary for runtime Environment authority and evidence. We do not merge by time, prompt text, model name, or Environment, and we do not make an Environment follow a Session; this preserves native resume continuity without inventing relationships or silently rerouting traffic.
