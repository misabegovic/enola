package diff

import "github.com/enola-labs/enola/internal/facts"

// FindingIdentity is facts.FindingIdentity, the identity the diff pairs a
// finding on; it lives beside the type so the engine stamps it without
// importing the diff.
func FindingIdentity(in facts.Insight) string { return facts.FindingIdentity(in) }

func WithIdentities[T any](v T) T { return facts.WithIdentities(v) }
