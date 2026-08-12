package colony

import (
	"errors"
	"testing"
)

func TestErrorsAsAPIError(t *testing.T) {
	// Generate one of every API error type
	errs := []error{
		newAPIError(401, "AUTH_INVALID_TOKEN", "msg", nil, nil),
		newAPIError(401, "AUTH_2FA_REQUIRED", "msg", nil, nil),
		newAPIError(401, "AUTH_2FA_INVALID", "msg", nil, nil),
		newAPIError(404, "", "msg", nil, nil),
		newAPIError(409, "", "msg", nil, nil),
		newAPIError(400, "", "msg", nil, nil),
		newAPIError(429, "", "msg", nil, nil),
		newAPIError(500, "", "msg", nil, nil),
		newAPIError(0, "", "msg", nil, nil),
	}

	for _, err := range errs {
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Errorf("errors.As(%T) failed to match *APIError", err)
		}
	}

	// Verify the chain still works for 2FA -> AuthError -> APIError
	twoFAErr := newAPIError(401, "AUTH_2FA_REQUIRED", "msg", nil, nil)
	var authErr *AuthError
	if !errors.As(twoFAErr, &authErr) {
		t.Errorf("errors.As(*TwoFactorRequiredError) failed to match *AuthError")
	}
}
