package runtimepersistence

import "github.com/vibe-agi/vibermate/internal/exchangecontent"

// The frozen root and the expected ordinal authorize a node, not its SQL row.
func validateContentNode(node storedTranscriptNode, expectedDigest string, expectedDepth int) error {
	parent := ""
	if node.parentDigest != nil {
		parent = *node.parentDigest
	}
	if expectedDepth < 1 || node.depth != expectedDepth || node.digest != expectedDigest ||
		!validStoredDigest(node.messageDigest) ||
		(node.depth > 1 && !validStoredDigest(parent)) ||
		(node.depth == 1 && node.parentDigest != nil) ||
		transcriptNodeDigest(parent, node.messageDigest) != node.digest {
		return exchangecontent.ErrInvalidEvidence
	}
	return nil
}
