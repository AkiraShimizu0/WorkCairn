package buildinfo

import "testing"

func TestCurrentNormalizesUnsetLinkerValues(t *testing.T) {
	previousVersion, previousCommit, previousBuildDate := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = previousVersion, previousCommit, previousBuildDate })
	Version, Commit, BuildDate = " ", "", "\t"

	got := Current()
	if got.Version != "dev" || got.Commit != "unknown" || got.BuildDate != "unknown" {
		t.Fatalf("Current() = %#v", got)
	}
}

func TestCurrentReturnsInjectedBuildMetadata(t *testing.T) {
	previousVersion, previousCommit, previousBuildDate := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = previousVersion, previousCommit, previousBuildDate })
	Version, Commit, BuildDate = "v1.0.0", "abc123", "2026-08-09T12:00:00Z"

	got := Current()
	if got.Version != Version || got.Commit != Commit || got.BuildDate != BuildDate {
		t.Fatalf("Current() = %#v", got)
	}
}
