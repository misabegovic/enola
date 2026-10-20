package server

import (
	"testing"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/pkg/plan"
)

// CountContractBuilds counts every store plan_check builds its contract from,
// for the life of the test.
func CountContractBuilds(t *testing.T) *int {
	t.Helper()
	n := 0
	build := contractStoreFor
	contractStoreFor = func(measured, declared []facts.Fact) *facts.Store {
		n++
		return build(measured, declared)
	}
	t.Cleanup(func() { contractStoreFor = build })
	return &n
}

// CountFanInBuilds counts every fan-in index plan_check builds, for the life of
// the test.
func CountFanInBuilds(t *testing.T) *int {
	t.Helper()
	n := 0
	build := fanInFor
	fanInFor = func(store *facts.Store) *plan.FanIn {
		n++
		return build(store)
	}
	t.Cleanup(func() { fanInFor = build })
	return &n
}
