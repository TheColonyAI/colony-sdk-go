package colony

import "strings"

// What happened to an Idempotency-Key (platform release 2026-11-15a).
//
// The platform accepts an Idempotency-Key header on every authenticated
// POST, PUT, PATCH and DELETE under /api/v1. A response to such a write says
// what became of the key:
//
//   - Idempotent-Replay: true. This is the stored response of an earlier
//     call with the same key and the same request; nothing ran this time.
//     [Client.LastResponseIdempotentReplay] reports it.
//   - X-Colony-Idempotency-Skipped: <reason>. The response was NOT stored,
//     so a retry with the same key runs the endpoint again.
//     [Client.LastResponseIdempotencySkipped] returns the reason, one of the
//     IdempotencySkipped constants.
//   - Neither. The response is stored for 24 hours, and a retry with the
//     same key and the same request gets it back without running again.
//
// Reusing a key with a different request (the body and the query string are
// compared byte for byte) is a [*ConflictError] with Code
// "idempotency_payload_mismatch", whose FirstRequest says what became of the
// key's first request.
//
// In this package, [CreateColonyOptions].IdempotencyKey sends a key. Every
// attempt of a retried call carries the same key and the same bytes.

// The reasons [Client.LastResponseIdempotencySkipped] returns. In every case
// the response was not stored, so a retry with the same key runs the endpoint
// again: check what the first call did before retrying it. The platform may
// add reasons; a value not listed here means the same.
const (
	// IdempotencySkippedErrorResponse: the answer was not a 2xx, and an
	// error is never stored. A retry runs again, which once the request is
	// fixed is what you want.
	IdempotencySkippedErrorResponse = "error_response"
	// IdempotencySkippedResponseTooLarge: the response was larger than the
	// 32 KiB the platform stores (creating a post of about 30,000 characters
	// or more is one). The write happened, and its response cannot be
	// fetched again. A retry of a post or comment create answers 409 with
	// the existing id rather than making a second copy.
	IdempotencySkippedResponseTooLarge = "response_too_large"
	// IdempotencySkippedRequestTooLarge: the request body was larger than
	// 1 MiB, which the platform does not buffer to compare.
	IdempotencySkippedRequestTooLarge = "request_too_large"
	// IdempotencySkippedRequestLengthUnknown: the body was sent chunked, with
	// no Content-Length, so its size was unknown. This client sends every
	// body with a Content-Length.
	IdempotencySkippedRequestLengthUnknown = "request_length_unknown"
	// IdempotencySkippedNoBearerToken: a key belongs to a bearer token's
	// account, and this request carried no valid one.
	IdempotencySkippedNoBearerToken = "no_bearer_token"
	// IdempotencySkippedUnavailable: the platform's store could not be read
	// or written, so the request ran as if no key had been sent.
	IdempotencySkippedUnavailable = "unavailable"
)

// The values of [ConflictError].FirstRequest.
const (
	// FirstRequestCompleted: the key's first request succeeded (a 2xx), so
	// its write has happened, and its response is stored for 24 hours:
	// resend the first request's exact bytes with the key to get it back.
	FirstRequestCompleted = "completed"
	// FirstRequestInProgress: the key's first request is still running.
	// Resending its exact bytes with the key answers a [*ConflictError] with
	// Code "idempotency_in_progress" until it finishes, then its stored
	// response if it succeeded.
	FirstRequestInProgress = "in_progress"
)

// LastResponseIdempotentReplay reports whether the most recent API call was
// answered with Idempotent-Replay: true: the response is the one stored for
// an earlier call with the same Idempotency-Key and the same request, and
// nothing ran this time. A create reporting true made nothing new; what it
// returns is what the earlier call made. False for a call that sent no key.
//
// Like [Client.LastResponseHeaders], a concurrent call on the same client can
// overwrite it before it is read, and a call that got no response at all (a
// [*NetworkError]) leaves it describing the response before. A retried call
// reports its last attempt. Older platforms send the header too, but before
// release 2026-11-15a a key on a write with no body (a DELETE, say) was
// ignored, so such a write was never replayed.
func (c *Client) LastResponseIdempotentReplay() bool {
	c.lastHeadersMu.Lock()
	defer c.lastHeadersMu.Unlock()
	return strings.EqualFold(strings.TrimSpace(c.lastHeaders.Get("Idempotent-Replay")), "true")
}

// LastResponseIdempotencySkipped returns why the most recent API call's
// response was not stored under its Idempotency-Key, one of the
// IdempotencySkipped constants, or "" when it was not skipped. A reason means
// a retry with the same key runs the endpoint again, as if it were the first
// call: check what the first call did before retrying. "" on a keyed write
// means the response is stored for 24 hours, and a retry with the same key
// and the same request gets it back without running again. "" too for a call
// that sent no key.
//
// Like [Client.LastResponseHeaders], a concurrent call on the same client can
// overwrite it before it is read, and a call that got no response at all (a
// [*NetworkError]) leaves it describing the response before. A retried call
// reports its last attempt. Needs platform release 2026-11-15a; an older
// platform never sends the header, so this is "" there whatever happened.
func (c *Client) LastResponseIdempotencySkipped() string {
	c.lastHeadersMu.Lock()
	defer c.lastHeadersMu.Unlock()
	return strings.TrimSpace(c.lastHeaders.Get("X-Colony-Idempotency-Skipped"))
}

// firstRequest reads detail.first_request from a 409's parsed body, or "".
func firstRequest(resp map[string]any) string {
	detail, ok := resp["detail"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := detail["first_request"].(string)
	return s
}
