package runtime

import (
	"net/http"
	"time"
)

// DefaultProviderRequestTimeout is the bounded Public Beta Runtime policy for
// one Provider request. Process and Domain packages receive only the resulting
// HTTPDoer; they do not own independent Provider deadlines.
const DefaultProviderRequestTimeout = 5 * time.Minute

// NewProviderHTTPClient constructs the shared Provider transport used by a
// process composition root. Callers validate explicit operator overrides;
// non-positive values fall back to the bounded product default rather than
// creating an unbounded client.
func NewProviderHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = DefaultProviderRequestTimeout
	}
	return &http.Client{Timeout: timeout}
}
