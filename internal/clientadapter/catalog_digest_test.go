package clientadapter_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

// knownCatalogDigests pins what each catalog revision contains.
//
// A CaptureRun records the catalog revision it consulted as evidence. If the
// catalog's content can change while its revision does not, every record
// citing that revision becomes a claim about something that no longer exists —
// and design 06 §4.2 is explicit that a catalog update must not silently
// widen what may be decrypted.
//
// Adding a release, changing a digest, or changing a launch recipe all change
// this value. When that happens, bump the catalog revision and add a line
// here: the new line is the review, and the old one stays so the history of
// what was trusted is readable.
var knownCatalogDigests = map[uint64]string{
	1: "hKM5Lx4gXv3F7WccGpMTq2npnmIVfCj6Pwd4wqeu1IA",
}

func TestTheCatalogCannotChangeWithoutItsRevision(t *testing.T) {
	t.Parallel()

	catalog := clientadapter.BuiltInCatalog()
	digest, err := catalog.Digest()
	if err != nil {
		t.Fatal(err)
	}
	revision := uint64(catalog.Revision())
	pinned, known := knownCatalogDigests[revision]
	if !known {
		t.Fatalf(
			"catalog revision %d has no pinned content; add it to "+
				"knownCatalogDigests with digest %q",
			revision,
			digest,
		)
	}
	if pinned != digest {
		t.Fatalf(
			"catalog revision %d changed content without changing revision\n"+
				"  pinned:  %s\n  current: %s\n"+
				"bump the catalog revision and pin the new digest",
			revision,
			pinned,
			digest,
		)
	}
}

// The digest describes the catalog's content, not the order it happens to be
// written in.
func TestTheCatalogDigestIsStableAcrossRuns(t *testing.T) {
	t.Parallel()

	first, err := clientadapter.BuiltInCatalog().Digest()
	if err != nil {
		t.Fatal(err)
	}
	second, err := clientadapter.BuiltInCatalog().Digest()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("digest = %q then %q", first, second)
	}
}
