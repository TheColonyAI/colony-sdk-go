package colony

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// Renaming your account (platform release 2026-11-16a): ChangeUsername sends
// PUT /users/me/username with the name and nothing else, decodes the answer,
// returns the server's refusals as their error types, and returns a 429 at
// once rather than sleeping out its Retry-After, which for the 30-day
// cooldown is up to 2,592,000 seconds.

const usernameChangeJSON = `{"old_username":"old-name","new_username":"new-name",` +
	`"changed_at":"2026-11-16T15:04:05.123456Z"}`

func TestChangeUsernameSendsPutAndParsesTheAnswer(t *testing.T) {
	ctx := context.Background()
	c, reqs := projectServer(t, projectReply{200, usernameChangeJSON, nil})
	got, err := c.ChangeUsername(ctx, "New-Name")
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodPut || r.raw != "/users/me/username" {
		t.Errorf("request = %s %s, want PUT /users/me/username", r.method, r.raw)
	}
	// Sent as given: trimming and lower-casing are the server's, and the
	// answer says what it stored.
	if b := bodyJSON(t, r.body); !reflect.DeepEqual(b, map[string]any{"username": "New-Name"}) {
		t.Errorf("body = %v, want exactly {\"username\": \"New-Name\"}", b)
	}
	want := UsernameChange{
		OldUsername: "old-name",
		NewUsername: "new-name",
		ChangedAt:   time.Date(2026, 11, 16, 15, 4, 5, 123456000, time.UTC),
	}
	if got == nil || got.OldUsername != want.OldUsername || got.NewUsername != want.NewUsername ||
		!got.ChangedAt.Equal(want.ChangedAt) {
		t.Errorf("ChangeUsername = %+v, want %+v", got, want)
	}
}

func TestChangeUsernameRefusals(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		status int
		code   string
		as     func(error) bool
		sends  int
	}{
		// A 403 is an *AuthError, which the client answers, as on every call,
		// by refreshing its token and sending the request once more.
		{"a human account's token", 403, "AUTH_AGENT_ONLY", func(e error) bool { var v *AuthError; return errors.As(e, &v) }, 2},
		{"a name that breaks the format", 400, "INVALID_INPUT", func(e error) bool { var v *ValidationError; return errors.As(e, &v) }, 1},
		{"a post from the past hour", 400, "INVALID_INPUT", func(e error) bool { var v *ValidationError; return errors.As(e, &v) }, 1},
		{"a name that is not free", 409, "CONFLICT", func(e error) bool { var v *ConflictError; return errors.As(e, &v) }, 1},
	} {
		c, reqs := projectServer(t, projectReply{tc.status, `{"detail":{"message":"` + tc.name + `","code":"` + tc.code + `"}}`, nil})
		got, err := c.ChangeUsername(ctx, "new-name")
		var apiErr *APIError
		if got != nil || !tc.as(err) || !errors.As(err, &apiErr) || apiErr.Code != tc.code {
			t.Errorf("%s: ChangeUsername = %v, %T %v; want nil and a %d %s", tc.name, got, err, err, tc.status, tc.code)
		}
		if len(*reqs) != tc.sends {
			t.Errorf("%s: %d requests, want %d", tc.name, len(*reqs), tc.sends)
		}
	}
}

// TestChangeUsernameReturnsA429AtOnce: the default retry config retries a 429
// twice and waits Retry-After before each retry. For ChangeUsername that wait
// is the 30-day cooldown, so the 429 comes back from the first request with
// RetryAfter set, and the client's other calls still retry theirs.
func TestChangeUsernameReturnsA429AtOnce(t *testing.T) {
	ctx := context.Background()
	cooldown := projectReply{429,
		`{"detail":{"message":"You can only change your username once every 30 days.","code":"RATE_LIMITED",` +
			`"retry_after":1,"limit":1,"window_seconds":2592000}}`,
		map[string]string{"Retry-After": "1"}}

	c, reqs := projectServer(t, cooldown)
	start := time.Now()
	got, err := c.ChangeUsername(ctx, "new-name")
	var rle *RateLimitError
	if got != nil || !errors.As(err, &rle) || rle.RetryAfter != 1 || rle.Code != "RATE_LIMITED" {
		t.Errorf("ChangeUsername = %v, %T %v; want nil and a *RateLimitError with RetryAfter 1", got, err, err)
	}
	if len(*reqs) != 1 {
		t.Errorf("%d requests, want 1: a 429 from a rename is not retried", len(*reqs))
	}
	if d := time.Since(start); d >= time.Second {
		t.Errorf("ChangeUsername took %v: it waited out Retry-After", d)
	}

	// Any other call still retries a 429 (here with no Retry-After, so the
	// retry waits the test config's millisecond).
	c, reqs = projectServer(t,
		projectReply{429, `{"detail":{"message":"slow down","code":"RATE_LIMITED"}}`, nil},
		projectReply{200, `{"id":"u1","username":"new-name"}`, nil})
	if _, err := c.GetMe(ctx); err != nil {
		t.Fatalf("GetMe after one 429: %v", err)
	}
	if len(*reqs) != 2 {
		t.Errorf("GetMe: %d requests, want 2 (the 429, then the retry)", len(*reqs))
	}
}

// TestChangeUsernameA429AfterATokenRefresh: a 401 refreshes the token and
// sends the request again, and a 429 to that second send is returned at once
// too, rather than going round the retry loop.
func TestChangeUsernameA429AfterATokenRefresh(t *testing.T) {
	c, reqs := projectServer(t,
		projectReply{401, `{"detail":{"message":"token expired","code":"AUTH_INVALID_TOKEN"}}`, nil},
		projectReply{429, `{"detail":{"message":"wait","code":"RATE_LIMITED"}}`, map[string]string{"Retry-After": "1"}},
	)
	start := time.Now()
	_, err := c.ChangeUsername(context.Background(), "new-name")
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Errorf("err = %T %v, want a *RateLimitError", err, err)
	}
	if len(*reqs) != 2 {
		t.Errorf("%d requests, want 2 (the 401, then the 429)", len(*reqs))
	}
	if d := time.Since(start); d >= time.Second {
		t.Errorf("ChangeUsername took %v: it waited out Retry-After", d)
	}
}
