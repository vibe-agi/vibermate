package access_test

import (
	"testing"
)

// A plan that names one upstream compiles one candidate, and it is the one
// every existing accessor already returns.
func TestASingleCandidatePlanCompilesOne(t *testing.T) {
	t.Parallel()

	compiler := testCompiler(t)
	accessID := newAccessID(t, "access-candidates")
	snapshot, err := compiler.Compile(testAggregate(t, accessID, 1, "Candidates"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CandidateCount() != 1 {
		t.Fatalf("candidates = %d", snapshot.CandidateCount())
	}
	candidate, found := snapshot.Candidate(0)
	if !found {
		t.Fatal("the first candidate is missing")
	}
	if candidate.Target().HTTPAuthority() !=
		snapshot.ProviderTargets()[0].HTTPAuthority() {
		t.Fatalf("candidate target = %+v", candidate.Target())
	}
	if candidate.CodecPlan().ID() != snapshot.CodecPlan().ID() {
		t.Fatal("the first candidate does not carry the plan's codec")
	}
	if _, beyond := snapshot.Candidate(1); beyond {
		t.Fatal("a one-candidate plan answered for a second")
	}
}
