package command

import (
	"github.com/enola-labs/enola/pkg/bootstrap"
	"github.com/enola-labs/enola/pkg/grade"
)

// Every read of a snapshot directory by a command goes through one of these, so a
// test can count them: each is a whole parse of facts.jsonl, the largest cost a
// one-shot command pays after starting.
var (
	loadSnapshotDir = bootstrap.LoadSnapshotDir
	restoreSnapshot = bootstrap.LoadDashboardSnapshot
)

// snapshotIsCurrent reports whether every member's on-disk snapshot describes its
// working tree under this build and config, so a pin can freeze what is already
// there, and when the newest of them was written; otherwise why not. It is the
// currency gate (grade.Members), which reads each member's own
// snapshot.meta.json and never the facts.
func snapshotIsCurrent(eng *bootstrap.Engine, repoPaths []string) (string, string) {
	members, stale := grade.Members(eng, repoPaths)
	if stale != "" {
		return "", stale
	}
	newest := ""
	for _, m := range members {
		if m.Recorded.GeneratedAt > newest {
			newest = m.Recorded.GeneratedAt
		}
	}
	if newest == "" {
		return "", "no members"
	}
	return newest, ""
}

var _ grade.Engine = (*bootstrap.Engine)(nil)
