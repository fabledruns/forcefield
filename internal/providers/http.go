package providers

import (
	"net/http"
	"time"
)

// defaultStreamTimeout bounds how long the client will wait for the
// first response headers. It does not bound the entire streamed body.
// For streaming completions the headers should arrive quickly (even if the
// model then thinks for a long time), so we bound only the header phase
// and let the body stream for as long as the provider is actively sending
// SSE. Without a header bound a dead cloud endpoint would hang until the
// OS TCP timeout (minutes to hours).
const defaultStreamTimeout = 120 * time.Second

func newDefaultClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: defaultStreamTimeout,
		},
	}
}
