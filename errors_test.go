package errors // or whatever your package name is

import (
	"errors"
	"testing"
)

func TestErrorsAsChain(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		target   error
		expected bool
	}{
		// Positive: All concrete errors should match *APIError
		{"NotFoundError as APIError", &NotFoundError{}, &APIError{}, true},
		{"ConflictError as APIError", &ConflictError{}, &APIError{}, true},
		{"AuthError as APIError", &AuthError{}, &APIError{}, true},

		// Positive: Errors should match their own concrete type
		{"NotFoundError as *NotFoundError", &NotFoundError{}, &NotFoundError{}, true},
		{"AuthError as *AuthError", &AuthError{}, &AuthError{}, true},

		// Positive: TwoFactorRequiredError should keep the intermediate AuthError hop
		{"TwoFactorRequiredError as *AuthError", &TwoFactorRequiredError{}, &AuthError{}, true},
		{"TwoFactorRequiredError as *APIError", &TwoFactorRequiredError{}, &APIError{}, true},

		// Negative: Siblings should NOT cross-match
		{"NotFoundError as *AuthError", &NotFoundError{}, &AuthError{}, false},
		{"ConflictError as *AuthError", &ConflictError{}, &AuthError{}, false},
		{"AuthError as *NotFoundError", &AuthError{}, &NotFoundError{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// errors.As requires a pointer to the target interface
			if errors.As(tt.err, &tt.target) != tt.expected {
				t.Errorf("errors.As(%T, %T) = %v, want %v", tt.err, tt.target, !tt.expected, tt.expected)
			}
		})
	}
}
