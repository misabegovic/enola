package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/enola-labs/enola/internal/engine"
	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/pkg/plan"
)

// contractStoreFor builds the store plan_check reads a contract from. A
// variable so a test can count the builds.
var contractStoreFor = plan.StoreWith

// fanInFor indexes a kept store's fan-in, built with the store. A variable so
// a test can count the builds.
var fanInFor = plan.IndexFanIn

// contractKey is what a kept contract store was built from: the snapshot, by
// id and fact count, the repository, and the declaration compiled from its
// working tree, by hash. Any of them moving means the store answers for
// something else.
type contractKey struct {
	snapshot string
	facts    int
	repo     string
	declared string
}

// keptContract holds the one contract store plan_check built last. One and
// not one per repository: each is a copy of every measured fact, and a union
// asked about two members in turn rebuilds rather than holding two copies.
type keptContract struct {
	mu    sync.Mutex
	key   contractKey
	store *facts.Store
	fanIn *plan.FanIn
}

// contractStore is the store plan_check reads repoPath's contract from, built
// from the loaded facts and the declaration in the working tree. The
// declaration is read on every call, since editing the law must be seen
// without regenerating; the facts are copied only when the snapshot, the
// repository or the compiled declaration differs from the kept store's. At
// 2.3M facts the copy cost plan_check about 2 s a call, and the fan-in read by
// scanning every fact for each member of a target another 20 s, so the store's
// fan-in index is kept with it.
func (s *Server) contractStore(repoPath, label string) (*facts.Store, *plan.FanIn, error) {
	declared, err := plan.Declared(repoPath, s.eng.Config().Intent[label])
	if err != nil {
		return nil, nil, err
	}
	encoded, err := json.Marshal(declared)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(encoded)
	store := s.eng.Store()
	key := contractKey{snapshot: s.loadedSnapshotID(), facts: store.Count(), repo: repoPath, declared: hex.EncodeToString(sum[:])}

	s.contract.mu.Lock()
	defer s.contract.mu.Unlock()
	if s.contract.store != nil && s.contract.key == key {
		return s.contract.store, s.contract.fanIn, nil
	}
	built := contractStoreFor(store.All(), declared)
	s.contract.key, s.contract.store, s.contract.fanIn = key, built, fanInFor(built)
	return built, s.contract.fanIn, nil
}

// dropContract lets the kept contract store go, when the graph it was copied
// from is replaced, so a server does not hold a copy of facts it no longer
// serves until plan_check is next asked.
func (s *Server) dropContract() {
	s.contract.mu.Lock()
	s.contract.store = nil
	s.contract.fanIn = nil
	s.contract.key = contractKey{}
	s.contract.mu.Unlock()
}

// memberDrift is the drift source for a tool anchored on one repository: that
// member's own recorded file hashes against its tree. In a union the published
// meta carries the last member's hashes, so every other member read as moved
// wholesale; with one repository loaded it is the engine's own reading.
type memberDrift struct{ s *Server }

func (m memberDrift) Drift(repoPath string) (engine.Drift, error) {
	if len(m.s.eng.RepoPaths()) > 1 {
		return m.s.eng.DriftFromMeta(repoPath, m.s.eng.MetaFor(repoPath))
	}
	return m.s.eng.Drift(repoPath)
}

// metaFor is the meta an anchored tool reports for repoPath: the member's own
// provenance in a union, and the snapshot's with one repository loaded.
func (s *Server) metaFor(repoPath string) facts.SnapshotMeta {
	if len(s.eng.RepoPaths()) > 1 {
		return s.eng.MetaFor(repoPath)
	}
	if snap := s.eng.Snapshot(); snap != nil {
		return snap.Meta
	}
	return facts.SnapshotMeta{}
}
