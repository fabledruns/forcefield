package orchevents

import "github.com/google/uuid"

// RunUUID returns a fresh run identifier. A separate tiny helper (over
// callers inlining uuid.NewString) keeps every id generation site the
// same shape and mockable in one place for tests.
func RunUUID() string {
	return uuid.NewString()
}

// ChildUUID returns a fresh child-run identifier for AgentSpawned /
// ChildCompleted correlation.
func ChildUUID() string {
	return uuid.NewString()
}
