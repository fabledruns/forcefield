package providers

import (
	"net/http"
	"time"
)

// defaultStreamTimeout bounds how long any provider request may take
// before it's aborted. It covers the entire exchange including reading the
// streamed body. Without a bound a stalled cloud stream hangs until the OS
// TCP timeout (minutes to hours). 120s is generous for normal streaming
// completions while still failing fast on dead connections.
const defaultStreamTimeout = 120 * time.Second

func newDefaultClient() *http.Client {
	return &http.Client{Timeout: defaultStreamTimeout}
}
