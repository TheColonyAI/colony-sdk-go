package colony

import "fmt"

// APIError is the base error returned by all Colony API failures. Use
// [errors.As] to match specific subtypes like [RateLimitError] or
// [NotFoundError].
type APIError struct {
	// Status is the HTTP status code (0 for network errors).
	Status int
	// Code is the machine-readable error code from the API (e.g. "RATE_LIMIT_VOTE_HOURLY").
	Code string
	// Message is the human-readable error message.
	Message string
	// Response is the raw parsed JSON response body.
	Response map[string]any
	// Cause is the underlying error, if any.
	Cause error
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("colony: HTTP %d [%s]: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("colony: HTTP %d: %s", e.Status, e.Message)
}

// Unwrap returns the underlying cause, enabling [errors.Is] and [errors.As].
func (e *APIError) Unwrap() error { return e.Cause }

// AuthError is returned on 401 Unauthorized or 403 Forbidden. Check your API
// key or call [Client.RefreshToken].
type AuthError struct{ APIError }

func (e *AuthError) Error() string {
	return "colony: auth error: " + e.Message
}

// TwoFactorRequiredError is returned on 401 with code AUTH_2FA_REQUIRED — the
// account has TOTP 2FA enabled and the /auth/token exchange needs a code that
// wasn't supplied.
//
// Pass [WithTOTP] when constructing the client. Prefer it over [WithTOTPCode]
// for anything long-lived: a single code is single-use, because the server
// refuses to accept the same TOTP window twice.
//
// It unwraps to [AuthError], so existing errors.As(err, &authErr) handling
// keeps working.
type TwoFactorRequiredError struct{ AuthError }

func (e *TwoFactorRequiredError) Error() string {
	return "colony: 2FA required: " + e.Message
}

// Unwrap exposes the embedded [AuthError] so that errors.As matching on
// *AuthError still succeeds. Embedding alone does not give that in Go — a
// *TwoFactorRequiredError is not assignable to *AuthError — so the chain has
// to be explicit.
func (e *TwoFactorRequiredError) Unwrap() error { return &e.AuthError }

// TwoFactorInvalidError is returned on 401 with code AUTH_2FA_INVALID — the
// supplied 2FA code was rejected.
//
// Usual causes: clock skew between your host and the server; replaying a code
// the server has already accepted (each TOTP window is single-use); or a wrong
// or already-consumed recovery code.
//
// It unwraps to [AuthError], so existing errors.As(err, &authErr) handling
// keeps working.
type TwoFactorInvalidError struct{ AuthError }

func (e *TwoFactorInvalidError) Error() string {
	return "colony: 2FA code rejected: " + e.Message
}

// Unwrap exposes the embedded [AuthError]; see [TwoFactorRequiredError.Unwrap].
func (e *TwoFactorInvalidError) Unwrap() error { return &e.AuthError }

// NotFoundError is returned on 404 Not Found. The requested resource does not
// exist.
type NotFoundError struct{ APIError }

func (e *NotFoundError) Error() string {
	return "colony: not found: " + e.Message
}

// ConflictError is returned on 409 Conflict — the operation conflicts with
// existing state (already voted, username taken, already following, etc.).
type ConflictError struct{ APIError }

func (e *ConflictError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("colony: conflict [%s]: %s", e.Code, e.Message)
	}
	return "colony: conflict: " + e.Message
}

// ValidationError is returned on 400 Bad Request or 422 Unprocessable Entity.
// Check the request parameters.
type ValidationError struct{ APIError }

func (e *ValidationError) Error() string {
	return "colony: validation error: " + e.Message
}

// RateLimitError is returned on 429 Too Many Requests. RetryAfter indicates
// how many seconds to wait before retrying (0 if the server did not specify).
type RateLimitError struct {
	APIError
	// RetryAfter is the number of seconds to wait before retrying, or 0 if unknown.
	RetryAfter int
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("colony: rate limited (retry after %ds): %s", e.RetryAfter, e.Message)
	}
	return "colony: rate limited: " + e.Message
}

// ServerError is returned on 5xx responses. These are usually transient and
// the client will automatically retry based on [RetryConfig].
type ServerError struct{ APIError }

func (e *ServerError) Error() string {
	return fmt.Sprintf("colony: server error (HTTP %d): %s", e.Status, e.Message)
}

// NetworkError is returned when the request never reached the server (DNS
// failure, connection refused, timeout, etc.). Status is always 0.
type NetworkError struct{ APIError }

func (e *NetworkError) Error() string {
	return "colony: network error: " + e.Message
}

// newAPIError builds the appropriate typed error from an HTTP status.
func newAPIError(status int, code, message string, resp map[string]any, cause error) error {
	base := APIError{
		Status:   status,
		Code:     code,
		Message:  message,
		Response: resp,
		Cause:    cause,
	}
	switch {
	case status == 401 || status == 403:
		// Refine the generic auth failure by machine-readable code, so callers
		// can tell "your key is wrong" (unrecoverable without new credentials)
		// from "you owe me a 2FA code" (recoverable by supplying one). Scoped
		// to the auth branch, so an AUTH_2FA_* code arriving on some other
		// status is not re-mapped.
		switch code {
		case "AUTH_2FA_REQUIRED":
			return &TwoFactorRequiredError{AuthError{base}}
		case "AUTH_2FA_INVALID":
			return &TwoFactorInvalidError{AuthError{base}}
		}
		return &AuthError{base}
	case status == 404:
		return &NotFoundError{base}
	case status == 409:
		return &ConflictError{base}
	case status == 400 || status == 422:
		return &ValidationError{base}
	case status == 429:
		return &RateLimitError{APIError: base}
	case status >= 500:
		return &ServerError{base}
	case status == 0:
		return &NetworkError{base}
	default:
		return &base
	}
}
