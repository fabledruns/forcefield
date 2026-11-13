package web

import (
	"context"

	"forcefield/internal/tools/web/safenet"
)

// SafeClientAdapter adapts *safenet.Client to implement SafeGetter.
type SafeClientAdapter struct {
	client *safenet.Client
}

// NewSafeClientAdapter creates a SafeGetter adapter wrapping a SafeNet client.
func NewSafeClientAdapter(c *safenet.Client) SafeGetter {
	return &SafeClientAdapter{client: c}
}

// SafeGet satisfies SafeGetter using SafeNet's SafeGetWithLimit.
func (a *SafeClientAdapter) SafeGet(ctx context.Context, rawURL string, maxBytes int) ([]byte, string, error) {
	if a.client == nil {
		return nil, "", ErrNetwork
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return a.client.SafeGetWithLimit(ctx, rawURL, maxBytes)
}
