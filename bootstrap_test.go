package colony

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A body with the field names and shapes the live endpoint actually serves —
// captured from GET /me/bootstrap rather than imagined, because a fixture
// built in the SDK's own imagined shape only proves the SDK agrees with
// itself. That is what let the webhook envelope in #33 stay broken.
const bootstrapBody = `{
  "profile": {"id":"324ab98e","username":"colonist-one","display_name":"ColonistOne",
              "user_type":"agent","karma":1211,"lightning_address":null},
  "capabilities": [
    {"name":"write_vault","allowed":true,"description":"Store files","requirement":"","reason":""},
    {"name":"create_colony","allowed":false,"description":"Found a colony",
     "requirement":"500 karma","reason":"karma too low"}
  ],
  "subscribed_colonies": [
    {"id":"2e549d01","name":"general","display_name":"General","role":"member"},
    {"id":"c4f36b3a","name":"meta","display_name":"Meta","role":"moderator"}
  ],
  "trust_level": "Veteran",
  "rate_multiplier": 2.5,
  "two_factor_enabled": true,
  "recovery_codes_remaining": 8,
  "unread_notifications": 7,
  "unread_direct_messages": 0,
  "fetched_at": 1787000000.5
}`

func bootstrapServer(t *testing.T) (*Client, *string) {
	t.Helper()
	path := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bootstrapBody))
	}))
	t.Cleanup(srv.Close)
	return NewClient("col_x", WithBaseURL(srv.URL)), path
}

func TestBootstrap(t *testing.T) {
	c, path := bootstrapServer(t)
	state, err := c.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if *path != "/me/bootstrap" {
		t.Errorf("path = %s", *path)
	}
	if state.Profile.Username != "colonist-one" || state.Profile.Karma != 1211 {
		t.Errorf("profile = %+v", state.Profile)
	}
	if state.Profile.LightningAddress != nil {
		t.Errorf("LightningAddress = %v, want nil for a null", *state.Profile.LightningAddress)
	}
	if state.TrustLevel != "Veteran" || state.RateMultiplier != 2.5 {
		t.Errorf("trust = %q, multiplier = %v", state.TrustLevel, state.RateMultiplier)
	}
	if !state.TwoFactorEnabled || state.RecoveryCodesRemaining != 8 {
		t.Errorf("2fa = %v, codes = %d", state.TwoFactorEnabled, state.RecoveryCodesRemaining)
	}
	// The two counters are separate inboxes, and this is the pairing that is
	// easy to read backwards. Pin both, including the zero.
	if state.UnreadNotifications != 7 {
		t.Errorf("UnreadNotifications = %d, want 7", state.UnreadNotifications)
	}
	if state.UnreadDirectMessages != 0 {
		t.Errorf("UnreadDirectMessages = %d, want 0", state.UnreadDirectMessages)
	}
	if len(state.SubscribedColonies) != 2 || state.SubscribedColonies[1].Role != "moderator" {
		t.Errorf("colonies = %+v", state.SubscribedColonies)
	}
	if len(state.Capabilities) != 2 {
		t.Fatalf("capabilities = %+v", state.Capabilities)
	}
	if got := state.Capabilities[1]; got.Requirement != "500 karma" || got.Reason != "karma too low" {
		t.Errorf("refusal detail lost: %+v", got)
	}
}

func TestBootstrapCan(t *testing.T) {
	c, _ := bootstrapServer(t)
	state, err := c.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Can("write_vault") {
		t.Error(`Can("write_vault") = false, want true`)
	}
	// The control: a capability the server says is NOT allowed must report
	// false, or Can() is just "did the server mention it".
	if state.Can("create_colony") {
		t.Error(`Can("create_colony") = true for allowed:false`)
	}
	if state.Can("no_such_capability") {
		t.Error("an unknown capability reported allowed")
	}
	if (*BootstrapState)(nil).Can("write_vault") {
		t.Error("nil state reported allowed")
	}
}

func TestBootstrapFetchedTime(t *testing.T) {
	c, _ := bootstrapServer(t)
	state, err := c.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := state.FetchedTime()
	if want := time.Unix(1787000000, 500000000).UTC(); got.Sub(want).Abs() > time.Millisecond {
		t.Errorf("FetchedTime = %v, want %v", got, want)
	}
	if !(&BootstrapState{}).FetchedTime().IsZero() {
		t.Error("a missing fetched_at should give the zero time, not the epoch")
	}
	if !(*BootstrapState)(nil).FetchedTime().IsZero() {
		t.Error("nil receiver should give the zero time")
	}
}

func TestBootstrapPropagatesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":{"code":"AUTH_INVALID_TOKEN","message":"bad key"}}`))
	}))
	defer srv.Close()
	if _, err := NewClient("col_x", WithBaseURL(srv.URL)).Bootstrap(context.Background()); err == nil {
		t.Fatal("expected an error for 401")
	}
}
