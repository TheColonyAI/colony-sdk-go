package colony

import "time"

// RetryConfig controls automatic retry behaviour.
type RetryConfig struct {
	// MaxRetries is the number of retries after the initial attempt. Default: 2.
	MaxRetries int
	// BaseDelay is the initial backoff delay. Default: 1s.
	BaseDelay time.Duration
	// MaxDelay caps the backoff delay. Default: 10s.
	MaxDelay time.Duration
	// RetryOn is the set of HTTP status codes that trigger a retry.
	// Default: {429, 502, 503, 504}.
	RetryOn map[int]bool
	// MaxRetryAfter is the longest Retry-After on a 429 that the client will
	// wait out before retrying. A 429 whose Retry-After is longer is returned
	// at once, as a [*RateLimitError] with RetryAfter set, so the caller can
	// decide: some limits are hourly or daily, and their Retry-After can be
	// hours. Default: 60s.
	//
	// Zero (or less) means no cap: every Retry-After is waited out, as before
	// this field existed, so a RetryConfig built as a literal without it keeps
	// its old behaviour. MaxDelay does not apply to Retry-After.
	MaxRetryAfter time.Duration
}

// DefaultRetry returns the default retry configuration.
func DefaultRetry() RetryConfig {
	return RetryConfig{
		MaxRetries:    2,
		BaseDelay:     1 * time.Second,
		MaxDelay:      10 * time.Second,
		MaxRetryAfter: 60 * time.Second,
		RetryOn: map[int]bool{
			429: true,
			502: true,
			503: true,
			504: true,
		},
	}
}

func (r RetryConfig) shouldRetry(status int) bool {
	return r.RetryOn[status]
}

// waitsOut reports whether a 429 with this Retry-After, in seconds, is
// within MaxRetryAfter. 0, an unknown Retry-After, always is: the retry then
// waits the backoff delay.
func (r RetryConfig) waitsOut(retryAfter int) bool {
	return r.MaxRetryAfter <= 0 || time.Duration(retryAfter)*time.Second <= r.MaxRetryAfter
}

func (r RetryConfig) delay(attempt int) time.Duration {
	d := r.BaseDelay
	for i := 0; i < attempt; i++ {
		d *= 2
	}
	if d > r.MaxDelay {
		d = r.MaxDelay
	}
	return d
}
