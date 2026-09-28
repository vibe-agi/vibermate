package capturerun

import (
	"strings"
	"testing"
)

func TestLaunchGitSnapshotIsExplicitAndFrozen(t *testing.T) {
	git := GitSnapshot{RepositorySource: "remote", RepositoryKey: strings.Repeat("a", 64), RepositoryName: "github.com/team/project", Branch: "main"}
	if err := git.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*GitSnapshot){
		func(v *GitSnapshot) { v.RepositorySource = "" },
		func(v *GitSnapshot) { v.RepositorySource = "unknown" },
		func(v *GitSnapshot) { v.RepositoryKey = "not-a-key" },
		func(v *GitSnapshot) { v.RepositoryName = "" },
		func(v *GitSnapshot) { v.Detached = true },
		func(v *GitSnapshot) { v.Branch = "" },
	} {
		invalid := git
		change(&invalid)
		if invalid.Validate() == nil {
			t.Fatalf("invalid snapshot accepted: %+v", invalid)
		}
	}
	record := DurableRecord{Runtime: RuntimeMetadata{GitAtLaunch: &git}}
	cloned, view, evidence := record.Runtime.Clone(), ViewOf(record), evidenceOf(record)
	git.Branch = "later"
	view.Runtime.GitAtLaunch.RepositoryName = "changed"
	if cloned.GitAtLaunch.Branch != "main" || view.Runtime.GitAtLaunch.Branch != "main" || evidence.Runtime.GitAtLaunch.Branch != "main" || cloned.GitAtLaunch.RepositoryName != "github.com/team/project" || evidence.Runtime.GitAtLaunch.RepositoryName != "github.com/team/project" {
		t.Fatal("caller mutation changed frozen launch evidence")
	}
}
