package colony

import (
	"errors"
	"testing"
)

func TestErrorsAsChain(t *testing.T) {
	tests := []struct {
		name string
		err  error
		as   func(error) bool
		want bool
	}{
		{"NotFoundError as *APIError", &NotFoundError{}, func(e error) bool { var x *APIError; return errors.As(e, &x) }, true},
		{"ConflictError as *APIError", &ConflictError{}, func(e error) bool { var x *APIError; return errors.As(e, &x) }, true},
		{"AuthError as *APIError", &AuthError{}, func(e error) bool { var x *APIError; return errors.As(e, &x) }, true},

		{"NotFoundError as *NotFoundError", &NotFoundError{}, func(e error) bool { var x *NotFoundError; return errors.As(e, &x) }, true},
		{"AuthError as *AuthError", &AuthError{}, func(e error) bool { var x *AuthError; return errors.As(e, &x) }, true},

		{"TwoFactorRequiredError as *AuthError", &TwoFactorRequiredError{}, func(e error) bool { var x *AuthError; return errors.As(e, &x) }, true},
		{"TwoFactorRequiredError as *APIError", &TwoFactorRequiredError{}, func(e error) bool { var x *APIError; return errors.As(e, &x) }, true},

		// TwoFactorInvalidError arms requested by maintainer
		{"TwoFactorInvalidError as *APIError", &TwoFactorInvalidError{}, func(e error) bool { var x *APIError; return errors.As(e, &x) }, true},
		{"TwoFactorInvalidError as *AuthError", &TwoFactorInvalidError{}, func(e error) bool { var x *AuthError; return errors.As(e, &x) }, true},

		// Siblings that share a parent — the cross-match most likely to be wrong.
		{"TwoFactorRequiredError as *TwoFactorInvalidError", &TwoFactorRequiredError{}, func(e error) bool { var x *TwoFactorInvalidError; return errors.As(e, &x) }, false},
		{"TwoFactorInvalidError as *TwoFactorRequiredError", &TwoFactorInvalidError{}, func(e error) bool { var x *TwoFactorRequiredError; return errors.As(e, &x) }, false},

		// The arms that carry the power: siblings must NOT cross-match.
		{"NotFoundError as *AuthError", &NotFoundError{}, func(e error) bool { var x *AuthError; return errors.As(e, &x) }, false},
		{"ConflictError as *AuthError", &ConflictError{}, func(e error) bool { var x *AuthError; return errors.As(e, &x) }, false},
		{"AuthError as *NotFoundError", &AuthError{}, func(e error) bool { var x *NotFoundError; return errors.As(e, &x) }, false},

		{"ValidationError as *APIError", &ValidationError{}, func(e error) bool { var x *APIError; return errors.As(e, &x) }, true},
		{"RateLimitError as *APIError", &RateLimitError{}, func(e error) bool { var x *APIError; return errors.As(e, &x) }, true},
		{"ServerError as *APIError", &ServerError{}, func(e error) bool { var x *APIError; return errors.As(e, &x) }, true},
		{"NetworkError as *APIError", &NetworkError{}, func(e error) bool { var x *APIError; return errors.As(e, &x) }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.as(tt.err); got != tt.want {
				t.Errorf("errors.As(%T, target) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
