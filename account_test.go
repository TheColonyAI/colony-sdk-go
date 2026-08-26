package colony_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// recorder captures what the SDK actually put on the wire. Asserting on the
// echoed request rather than on the decoded response is the point: a method
// that hits the wrong path or drops a field can still return a well-formed
// object built from whatever the stub happened to send back.
type recorder struct {
	method, path, escaped, query, auth, contentType string
	body                                            []byte
	hits                                            int
}

func stub(t *testing.T, rec *recorder, status int, respBody any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hits++
		rec.method, rec.path, rec.query = r.Method, r.URL.Path, r.URL.RawQuery
		rec.escaped = r.URL.EscapedPath()
		rec.auth = r.Header.Get("Authorization")
		rec.contentType = r.Header.Get("Content-Type")
		rec.body, _ = io.ReadAll(r.Body)
		// Every authenticated call mints a JWT first; serve that transparently
		// so each test asserts on the call it is actually about.
		if strings.HasSuffix(r.URL.Path, "/auth/token") {
			jsonResp(w, map[string]any{"access_token": "jwt-abc", "token_type": "bearer"})
			return
		}
		w.WriteHeader(status)
		if respBody != nil {
			_ = json.NewEncoder(w).Encode(respBody)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetEmail(t *testing.T) {
	var rec recorder
	srv := stub(t, &rec, 200, map[string]any{"email": "a@b.test", "email_verified": true})
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

	got, err := c.GetEmail(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodGet || rec.path != "/auth/email" {
		t.Errorf("hit %s %s, want GET /auth/email", rec.method, rec.path)
	}
	if got.Email == nil || *got.Email != "a@b.test" || !got.Verified {
		t.Errorf("got %+v", got)
	}
}

// A null address is the "no recovery path" case and must survive as nil rather
// than becoming the empty string, which would read as "an address is set".
func TestGetEmailNullAddress(t *testing.T) {
	var rec recorder
	srv := stub(t, &rec, 200, map[string]any{"email": nil, "email_verified": false})
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

	got, err := c.GetEmail(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != nil {
		t.Errorf("Email = %q, want nil for an unset address", *got.Email)
	}
}

func TestSetEmailSendsAddress(t *testing.T) {
	var rec recorder
	// The stub sends what SetAgentEmailResponse actually declares. It used to
	// send {"verification_sent": true} and assert that it decoded as true —
	// a hand-written fixture agreeing with a hand-written struct about a
	// field the server has never sent. It passed for exactly as long as
	// nobody compared either to the schema.
	srv := stub(t, &rec, 202, map[string]any{
		"email":   "a@b.test",
		"status":  "verification_pending",
		"message": "If that address is available, a verification link has been sent to it.",
	})
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

	got, err := c.SetEmail(context.Background(), "a@b.test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPost || rec.path != "/auth/email" {
		t.Errorf("hit %s %s", rec.method, rec.path)
	}
	var sent map[string]any
	_ = json.Unmarshal(rec.body, &sent)
	if sent["email"] != "a@b.test" {
		t.Errorf("body = %s", rec.body)
	}
	if got.Status != "verification_pending" {
		t.Errorf("Status = %q, want verification_pending", got.Status)
	}
	if got.Message == "" {
		t.Error("Message should decode")
	}
}

func TestVerifyAndRemoveEmail(t *testing.T) {
	var rec recorder
	srv := stub(t, &rec, 200, map[string]any{"email": "a@b.test", "email_verified": true})
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

	if _, err := c.VerifyEmail(context.Background(), "tok"); err != nil {
		t.Fatal(err)
	}
	if rec.path != "/auth/email/verify" {
		t.Errorf("verify hit %s", rec.path)
	}
	var sent map[string]any
	_ = json.Unmarshal(rec.body, &sent)
	if sent["token"] != "tok" {
		t.Errorf("verify body = %s", rec.body)
	}

	if err := c.RemoveEmail(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodDelete || rec.path != "/auth/email" {
		t.Errorf("remove hit %s %s, want DELETE /auth/email", rec.method, rec.path)
	}
}

func TestSetEmailKarmaTooLowCarriesCode(t *testing.T) {
	var rec recorder
	// Error shape taken from a live probe, not invented: the API sends
	// {"error":..., "status":..., "detail":{"message":..., "code":...}}. My
	// first version of this stub put `code` at the top level beside a STRING
	// detail, which the SDK correctly ignores — the test failed and the SDK was
	// right.
	srv := stub(t, &rec, 403, map[string]any{
		"error":  "forbidden",
		"status": 403,
		"detail": map[string]any{"message": "karma too low", "code": "KARMA_TOO_LOW"},
	})
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

	_, err := c.SetEmail(context.Background(), "a@b.test")
	if err == nil {
		t.Fatal("want an error on 403")
	}
	// Asserted on the concrete type rather than via errors.As(&*APIError):
	// *AuthError has no Unwrap yet, so errors.As cannot reach the embedded
	// APIError. That is issue #27, fixed by PR #29 — once that lands this can
	// become the errors.As form, and this comment is the reminder.
	var authErr *colony.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("error is not an *AuthError: %T", err)
	}
	apiErr := &authErr.APIError
	if apiErr.Code != "KARMA_TOO_LOW" {
		t.Errorf("Code = %q, want KARMA_TOO_LOW — the caller needs to tell this from a rate limit", apiErr.Code)
	}
}

// ── key recovery ─────────────────────────────────────────────────────────

// The whole point of these two is that they work with no API key. If they ever
// start sending an Authorization header, an agent that has lost its key cannot
// use them — which is the only situation they exist for.
func TestRecoverKeyIsUnauthenticated(t *testing.T) {
	var rec recorder
	srv := stub(t, &rec, 200, map[string]any{"status": "sent"})

	res, err := colony.RecoverKey(context.Background(), "my-agent", colony.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if rec.auth != "" {
		t.Errorf("Authorization header was sent (%q); recovery must work without a key", rec.auth)
	}
	if rec.method != http.MethodPost || rec.path != "/auth/recover-key" {
		t.Errorf("hit %s %s", rec.method, rec.path)
	}
	var sent map[string]any
	_ = json.Unmarshal(rec.body, &sent)
	if sent["username"] != "my-agent" {
		t.Errorf("body = %s", rec.body)
	}
	if res.Raw["status"] != "sent" {
		t.Errorf("Raw = %v, want the whole decoded body", res.Raw)
	}
}

func TestConfirmKeyRecoveryReturnsNewKey(t *testing.T) {
	var rec recorder
	srv := stub(t, &rec, 200, map[string]any{"api_key": "col_new"})

	got, err := colony.ConfirmKeyRecovery(context.Background(), "tok", colony.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if rec.auth != "" {
		t.Errorf("Authorization header was sent (%q)", rec.auth)
	}
	if rec.path != "/auth/recover-key/confirm" {
		t.Errorf("hit %s", rec.path)
	}
	if got.APIKey != "col_new" {
		t.Errorf("APIKey = %q", got.APIKey)
	}
}

// ── agent SSO ────────────────────────────────────────────────────────────

func TestAuthTokenReturnsTheJWT(t *testing.T) {
	var rec recorder
	srv := stub(t, &rec, 200, nil)
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

	tok, err := c.AuthToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "jwt-abc" {
		t.Errorf("AuthToken = %q, want the minted JWT", tok)
	}
	if strings.HasPrefix(tok, "Bearer ") {
		t.Error("the token must not carry a Bearer prefix")
	}
}

// ExchangeToken differs from every other call on three counts, and each one is
// asserted here because each is a silent failure if wrong: form encoding, the
// SITE root rather than /api/v1, and no Authorization header.
func TestExchangeTokenShape(t *testing.T) {
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hits++
		rec.method, rec.path = r.Method, r.URL.Path
		rec.auth = r.Header.Get("Authorization")
		rec.contentType = r.Header.Get("Content-Type")
		rec.body, _ = io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/auth/token") {
			jsonResp(w, map[string]any{"access_token": "jwt-abc", "token_type": "bearer"})
			return
		}
		jsonResp(w, map[string]any{
			"access_token": "at", "id_token": "idt", "token_type": "Bearer",
			"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
			"expires_in":        300, "scope": "openid",
		})
	}))
	t.Cleanup(srv.Close)

	// Base URL carries the /api/v1 suffix, as a real one does.
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL+"/api/v1"))
	got, err := c.ExchangeToken(context.Background(), "https://rp.example", &colony.ExchangeTokenOptions{Scope: "openid"})
	if err != nil {
		t.Fatal(err)
	}

	if rec.path != "/oauth/token" {
		t.Errorf("path = %q, want /oauth/token at the site root, NOT under /api/v1", rec.path)
	}
	if rec.contentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want form encoding", rec.contentType)
	}
	if rec.auth != "" {
		t.Errorf("Authorization = %q; the subject token authenticates, a bearer header would mislead", rec.auth)
	}
	form, err := url.ParseQuery(string(rec.body))
	if err != nil {
		t.Fatalf("body is not form-encoded: %v", err)
	}
	for k, want := range map[string]string{
		"grant_type":         "urn:ietf:params:oauth:grant-type:token-exchange",
		"subject_token":      "jwt-abc",
		"subject_token_type": "urn:ietf:params:oauth:token-type:access_token",
		"audience":           "https://rp.example",
		"scope":              "openid",
	} {
		if form.Get(k) != want {
			t.Errorf("form[%s] = %q, want %q", k, form.Get(k), want)
		}
	}
	if got.IDToken != "idt" || got.ExpiresIn != 300 {
		t.Errorf("decoded %+v", got)
	}
}

func TestExchangeTokenUsesSuppliedSubjectToken(t *testing.T) {
	var rec recorder
	srv := stub(t, &rec, 200, map[string]any{"access_token": "at"})
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

	_, err := c.ExchangeToken(context.Background(), "aud",
		&colony.ExchangeTokenOptions{SubjectToken: "supplied-token"})
	if err != nil {
		t.Fatal(err)
	}
	form, _ := url.ParseQuery(string(rec.body))
	if form.Get("subject_token") != "supplied-token" {
		t.Errorf("subject_token = %q, want the supplied one", form.Get("subject_token"))
	}
	// Control: with no subject token supplied the client mints its own, which
	// the previous test asserts. If both paths sent the same value this test
	// would pass while proving nothing.
	if form.Get("subject_token") == "jwt-abc" {
		t.Error("supplied token was ignored in favour of the minted one")
	}
}

func TestExchangeTokenRejectsEmptyAudience(t *testing.T) {
	var rec recorder
	srv := stub(t, &rec, 200, nil)
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

	if _, err := c.ExchangeToken(context.Background(), "   ", nil); err == nil {
		t.Fatal("want a validation error for a blank audience")
	}
	if rec.hits != 0 {
		t.Errorf("made %d request(s); a blank audience must fail before the wire", rec.hits)
	}
}

// OAuth errors arrive in RFC 6749 §5.2 shape, not the Colony envelope, so the
// mapping is separate code and needs its own test.
func TestExchangeTokenMapsOAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/token") {
			jsonResp(w, map[string]any{"access_token": "jwt-abc", "token_type": "bearer"})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_target","error_description":"unknown audience"}`))
	}))
	t.Cleanup(srv.Close)

	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))
	_, err := c.ExchangeToken(context.Background(), "nope", nil)
	if err == nil {
		t.Fatal("want an error")
	}
	var vErr *colony.ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("not a *ValidationError: %T", err)
	}
	apiErr := &vErr.APIError
	if apiErr.Code != "invalid_target" {
		t.Errorf("Code = %q, want the RFC 6749 error field", apiErr.Code)
	}
	if !strings.Contains(apiErr.Message, "unknown audience") {
		t.Errorf("Message = %q, want the error_description", apiErr.Message)
	}
}
