package colony_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// Agent TOTP 2FA: the management surface plus the /auth/token code plumbing.
//
// Two behaviours here are load-bearing and easy to regress:
//
//   - the token-exchange body only grows a totp_code when one is configured,
//     so the request is unchanged for the (vast majority of) accounts without
//     2FA; and
//   - a code from WithTOTPCode is single-use — the server accepts each TOTP
//     window exactly once, so silently replaying it would surface as an opaque
//     AUTH_2FA_INVALID on a later refresh.
//
// Everything is driven through a real httptest server, so what is asserted is
// what actually goes on the wire.

// totpKeys gives every totpServer client an API key of its own.
//
// The client's token cache is process-wide and keyed by API key plus base
// URL, and an httptest server's URL is 127.0.0.1 with a port the OS hands out
// again once an earlier server has closed. With one shared key, a test whose
// server landed on a reused port found an earlier test's cached token,
// skipped the exchange, and never called its TOTP provider:
// TestTOTPProviderErrorAbortsExchange then failed with "not found: no route"
// (1 full -race run of master in 8, measured 2026-10-21). A key per client
// keeps every exchange this file asserts on a real one;
// TestReusedAddressSharesTokensOnlyWithinAKey pins the cache behaviour the
// fix relies on.
var totpKeys atomic.Int64

// lastTOTPKey is the key totpServer issued most recently. Read it straight
// after the call: the tests in this file do not run in parallel.
func lastTOTPKey() string { return fmt.Sprintf("col_test_totp_%d", totpKeys.Load()) }

// totpServer records every /auth/token body and serves a canned route.
func totpServer(t *testing.T, opts []colony.Option, routes map[string]http.HandlerFunc) (*colony.Client, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	bodies := []map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" && r.Method == http.MethodPost {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			bodies = append(bodies, b)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "test-jwt"})
			return
		}
		if h, ok := routes[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": map[string]any{"message": "no route"}})
	}))
	t.Cleanup(srv.Close)

	base := []colony.Option{
		colony.WithBaseURL(srv.URL),
		colony.WithTimeout(5 * time.Second),
		colony.WithRetry(colony.RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}}),
	}
	key := fmt.Sprintf("col_test_totp_%d", totpKeys.Add(1))
	return colony.NewClient(key, append(base, opts...)...), &bodies
}

func okJSON(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(v) }
}

func TestTokenBodyOmitsTOTPWhenUnset(t *testing.T) {
	client, bodies := totpServer(t, nil, map[string]http.HandlerFunc{
		"GET /auth/2fa/status": okJSON(map[string]any{"enabled": false, "recovery_codes_remaining": 0}),
	})
	key := lastTOTPKey()
	if _, err := client.Get2FAStatus(context.Background()); err != nil {
		t.Fatalf("Get2FAStatus: %v", err)
	}
	if len(*bodies) != 1 {
		t.Fatalf("expected 1 token exchange, got %d", len(*bodies))
	}
	b := (*bodies)[0]
	if _, present := b["totp_code"]; present {
		t.Errorf("totp_code must be absent when no TOTP is configured, got %v", b)
	}
	if b["api_key"] != key {
		t.Errorf("api_key = %v, want %s", b["api_key"], key)
	}
}

func TestTokenBodyCarriesStaticCodeOnce(t *testing.T) {
	client, bodies := totpServer(t, []colony.Option{colony.WithTOTPCode("123456")},
		map[string]http.HandlerFunc{
			"GET /auth/2fa/status": okJSON(map[string]any{"enabled": true, "recovery_codes_remaining": 8}),
		})
	if _, err := client.Get2FAStatus(context.Background()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if got := (*bodies)[0]["totp_code"]; got != "123456" {
		t.Errorf("totp_code = %v, want 123456", got)
	}

	// Force re-authentication: the code is spent, so this must fail with
	// something actionable rather than replay a window the server will reject.
	client.RefreshToken()
	_, err := client.Get2FAStatus(context.Background())
	if err == nil {
		t.Fatal("expected an error on the second exchange, got nil")
	}
	var required *colony.TwoFactorRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("error = %T (%v), want *TwoFactorRequiredError", err, err)
	}
	if len(*bodies) != 1 {
		t.Errorf("spent code must not be sent: got %d token exchanges, want 1", len(*bodies))
	}
}

func TestTokenBodyRefreshesCallableCode(t *testing.T) {
	var n int
	client, bodies := totpServer(t, []colony.Option{colony.WithTOTP(func() (string, error) {
		n++
		return []string{"111111", "222222"}[n-1], nil
	})}, map[string]http.HandlerFunc{
		"GET /auth/2fa/status": okJSON(map[string]any{"enabled": true, "recovery_codes_remaining": 8}),
	})

	if _, err := client.Get2FAStatus(context.Background()); err != nil {
		t.Fatalf("first: %v", err)
	}
	client.RefreshToken()
	if _, err := client.Get2FAStatus(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := (*bodies)[0]["totp_code"]; got != "111111" {
		t.Errorf("first code = %v, want 111111", got)
	}
	if got := (*bodies)[1]["totp_code"]; got != "222222" {
		t.Errorf("second code = %v, want 222222", got)
	}
}

func TestTOTPProviderErrorAbortsExchange(t *testing.T) {
	sentinel := errors.New("authenticator unavailable")
	client, bodies := totpServer(t, []colony.Option{colony.WithTOTP(func() (string, error) {
		return "", sentinel
	})}, nil)

	_, err := client.Get2FAStatus(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap the provider's error", err)
	}
	if len(*bodies) != 0 {
		t.Errorf("no token exchange should be attempted, got %d", len(*bodies))
	}
}

func TestAuthErrorRefinedByCode(t *testing.T) {
	cases := []struct {
		code       string
		wantIs2FA  bool
		wantInval  bool
		wantStatus int
	}{
		{"AUTH_2FA_REQUIRED", true, false, 401},
		{"AUTH_2FA_INVALID", false, true, 401},
		{"AUTH_INVALID_TOKEN", false, false, 401},
		{"", false, false, 401},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			client, _ := totpServer(t, nil, map[string]http.HandlerFunc{
				"GET /auth/2fa/status": func(w http.ResponseWriter, _ *http.Request) {
					detail := map[string]any{"message": "nope"}
					if tc.code != "" {
						detail["code"] = tc.code
					}
					w.WriteHeader(tc.wantStatus)
					_ = json.NewEncoder(w).Encode(map[string]any{"detail": detail})
				},
			})
			_, err := client.Get2FAStatus(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}

			var required *colony.TwoFactorRequiredError
			var invalid *colony.TwoFactorInvalidError
			if got := errors.As(err, &required); got != tc.wantIs2FA {
				t.Errorf("errors.As(*TwoFactorRequiredError) = %v, want %v (err %T)", got, tc.wantIs2FA, err)
			}
			if got := errors.As(err, &invalid); got != tc.wantInval {
				t.Errorf("errors.As(*TwoFactorInvalidError) = %v, want %v (err %T)", got, tc.wantInval, err)
			}

			// Every 401 must remain matchable as *AuthError so existing
			// handling keeps working. Go embedding does not give this for
			// free — it relies on the explicit Unwrap.
			var auth *colony.AuthError
			if !errors.As(err, &auth) {
				t.Errorf("errors.As(*AuthError) = false for %T; 2FA errors must stay catchable as auth errors", err)
			}
		})
	}
}

func TestNonAuthStatusNotRefined(t *testing.T) {
	// A 2FA-ish code on a non-auth status must not be re-mapped.
	client, _ := totpServer(t, nil, map[string]http.HandlerFunc{
		"GET /auth/2fa/status": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"detail": map[string]any{"message": "x", "code": "AUTH_2FA_INVALID"},
			})
		},
	})
	_, err := client.Get2FAStatus(context.Background())
	var invalid *colony.TwoFactorInvalidError
	if errors.As(err, &invalid) {
		t.Errorf("a 404 carrying AUTH_2FA_INVALID must not become a TwoFactorInvalidError, got %T", err)
	}
	var notFound *colony.NotFoundError
	if !errors.As(err, &notFound) {
		t.Errorf("error = %T, want *NotFoundError", err)
	}
}

func TestTwoFactorMethods(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]map[string]any{}
	record := func(key string, resp any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			seen[key] = b
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(resp)
		}
	}
	client, _ := totpServer(t, nil, map[string]http.HandlerFunc{
		"GET /auth/2fa/status": okJSON(map[string]any{"enabled": true, "recovery_codes_remaining": 8}),
		"POST /auth/2fa/enroll": okJSON(map[string]any{
			"secret": "JBSWY3DP", "otpauth_uri": "otpauth://totp/x", "ticket": "t.sig",
		}),
		"POST /auth/2fa/confirm": record("confirm", map[string]any{
			"enabled": true, "recovery_codes": []string{"a", "b"}, "recovery_codes_remaining": 2,
		}),
		"POST /auth/2fa/disable": record("disable", map[string]any{
			"enabled": false, "recovery_codes_remaining": 0,
		}),
		"POST /auth/2fa/recovery-codes/regenerate": record("regen", map[string]any{
			"recovery_codes": []string{"x"}, "recovery_codes_remaining": 1,
		}),
	})
	ctx := context.Background()

	status, err := client.Get2FAStatus(ctx)
	if err != nil || !status.Enabled || status.RecoveryCodesRemaining != 8 {
		t.Fatalf("Get2FAStatus = %+v, %v", status, err)
	}

	enroll, err := client.Enroll2FA(ctx)
	if err != nil || enroll.Secret != "JBSWY3DP" || enroll.OtpauthURI != "otpauth://totp/x" || enroll.Ticket != "t.sig" {
		t.Fatalf("Enroll2FA = %+v, %v", enroll, err)
	}

	confirm, err := client.Confirm2FA(ctx, "SECRET", "ticket.sig", "123456")
	if err != nil || len(confirm.RecoveryCodes) != 2 {
		t.Fatalf("Confirm2FA = %+v, %v", confirm, err)
	}
	if b := seen["confirm"]; b["secret"] != "SECRET" || b["ticket"] != "ticket.sig" || b["code"] != "123456" {
		t.Errorf("Confirm2FA body = %v", b)
	}

	disable, err := client.Disable2FA(ctx, "123456")
	if err != nil || disable.Enabled {
		t.Fatalf("Disable2FA = %+v, %v", disable, err)
	}
	if b := seen["disable"]; b["code"] != "123456" {
		t.Errorf("Disable2FA body = %v", b)
	}

	regen, err := client.RegenerateRecoveryCodes(ctx, "123456")
	if err != nil || len(regen.RecoveryCodes) != 1 {
		t.Fatalf("RegenerateRecoveryCodes = %+v, %v", regen, err)
	}
	if b := seen["regen"]; b["code"] != "123456" {
		t.Errorf("RegenerateRecoveryCodes body = %v", b)
	}
}

// serverAt starts a server on addr that answers the token exchange and 404s
// everything else, counting exchanges. Binding a chosen address is how this
// file reproduces port reuse on demand rather than by chance.
func serverAt(t *testing.T, addr string, exchanges *atomic.Int32) *httptest.Server {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" && r.Method == http.MethodPost {
			exchanges.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "test-jwt"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": map[string]any{"message": "no route"}})
	}))
	_ = srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	return srv
}

// TestReusedAddressSharesTokensOnlyWithinAKey reproduces the flake behind
// totpKeys deterministically. A token cached against one server is found by
// a later client with the SAME key on the SAME address (the cache working as
// designed), so that client never calls its TOTP provider. A client with a
// different key on that address does the exchange and calls it.
func TestReusedAddressSharesTokensOnlyWithinAKey(t *testing.T) {
	opts := func(u string) []colony.Option {
		return []colony.Option{colony.WithBaseURL(u), colony.WithTimeout(5 * time.Second),
			colony.WithRetry(colony.RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}})}
	}
	shared := fmt.Sprintf("col_test_reuse_%d", totpKeys.Add(1))

	var exchanges atomic.Int32
	first := serverAt(t, "127.0.0.1:0", &exchanges)
	addr := first.Listener.Addr().String()
	_, _ = colony.NewClient(shared, opts(first.URL)...).Get2FAStatus(context.Background())
	first.Close()
	if exchanges.Load() != 1 {
		t.Fatalf("the first client should exchange its key once, got %d", exchanges.Load())
	}

	second := serverAt(t, addr, &exchanges)
	defer second.Close()
	if second.URL != first.URL {
		t.Fatalf("could not reuse %s: got %s", first.URL, second.URL)
	}
	sentinel := errors.New("authenticator unavailable")
	provider := colony.WithTOTP(func() (string, error) { return "", sentinel })

	_, err := colony.NewClient(shared, append(opts(second.URL), provider)...).Get2FAStatus(context.Background())
	if errors.Is(err, sentinel) || exchanges.Load() != 1 {
		t.Errorf("same key, same address: want the cached token (no exchange, provider not called), got err=%v exchanges=%d",
			err, exchanges.Load())
	}

	other := fmt.Sprintf("col_test_reuse_%d", totpKeys.Add(1))
	_, err = colony.NewClient(other, append(opts(second.URL), provider)...).Get2FAStatus(context.Background())
	if !errors.Is(err, sentinel) {
		t.Errorf("a different key must not see the cached token: want the provider's error, got %v", err)
	}
}
