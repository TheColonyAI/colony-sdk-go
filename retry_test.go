package colony

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The retry loop refreshes the token once on a 401 and sends the request
// again. What that second send gets back is classified like any other answer:
// a refusal is returned at once, and only a status in RetryOn goes round the
// retry loop. Before this, every answer to the second send except another 401
// went round the loop, so a 400, 404, 409 or 422 was sent a third time.

func TestARefusalAfterATokenRefreshIsNotSentAgain(t *testing.T) {
	expired := projectReply{401, `{"detail":{"message":"token expired","code":"AUTH_INVALID_TOKEN"}}`, nil}
	for _, tc := range []struct {
		name   string
		status int
		code   string
		as     func(error) bool
	}{
		{"a 400", 400, "INVALID_INPUT", func(e error) bool { var v *ValidationError; return errors.As(e, &v) }},
		{"a 404", 404, "NOT_FOUND", func(e error) bool { var v *NotFoundError; return errors.As(e, &v) }},
		{"a 409", 409, "CONFLICT", func(e error) bool { var v *ConflictError; return errors.As(e, &v) }},
		{"a 422", 422, "VALIDATION_ERROR", func(e error) bool { var v *ValidationError; return errors.As(e, &v) }},
	} {
		refused := projectReply{tc.status, `{"detail":{"message":"refused","code":"` + tc.code + `"}}`, nil}
		c, reqs := projectServer(t, expired, refused)
		_, err := c.ChangeUsername(context.Background(), "new-name")
		if !tc.as(err) {
			t.Errorf("%s after a refresh: err = %T %v", tc.name, err, err)
		}
		if len(*reqs) != 2 {
			t.Errorf("%s after a refresh: %d requests, want 2 (the 401, then the refused one, not sent again)", tc.name, len(*reqs))
		}

		// The same refusal without the 401 first: one request, as before.
		c, reqs = projectServer(t, refused)
		if _, err := c.ChangeUsername(context.Background(), "new-name"); !tc.as(err) {
			t.Errorf("%s: err = %T %v", tc.name, err, err)
		}
		if len(*reqs) != 1 {
			t.Errorf("%s: %d requests, want 1", tc.name, len(*reqs))
		}
	}
}

// A retryable answer to the request after a refresh is still retried.
func TestARetryableAnswerAfterATokenRefreshIsStillRetried(t *testing.T) {
	expired := projectReply{401, `{"detail":{"message":"token expired","code":"AUTH_INVALID_TOKEN"}}`, nil}
	me := projectReply{200, `{"id":"u1","username":"me"}`, nil}
	for _, tc := range []struct {
		name string
		then projectReply
	}{
		{"a 503", projectReply{503, `{"detail":"unavailable"}`, nil}},
		{"a 429 with no Retry-After", projectReply{429, `{"detail":{"message":"slow down","code":"RATE_LIMITED"}}`, nil}},
	} {
		c, reqs := projectServer(t, expired, tc.then, me)
		if _, err := c.GetMe(context.Background()); err != nil {
			t.Errorf("%s after a refresh, then a 200: GetMe = %v", tc.name, err)
		}
		if len(*reqs) != 3 {
			t.Errorf("%s after a refresh: %d requests, want 3 (the 401, %s, the retry)", tc.name, len(*reqs), tc.name)
		}
	}
}

// --- RetryConfig.MaxRetryAfter ---

func TestDefaultRetryCapsRetryAfterAtSixtySeconds(t *testing.T) {
	if got := DefaultRetry().MaxRetryAfter; got != 60*time.Second {
		t.Errorf("DefaultRetry().MaxRetryAfter = %v, want 60s", got)
	}
}

// capServer is projectServer with MaxRetryAfter set to max.
func capServer(t *testing.T, max time.Duration, script ...projectReply) (*Client, *[]projectReq) {
	t.Helper()
	c, reqs := projectServer(t, script...)
	c.retry.MaxRetryAfter = max
	return c, reqs
}

func limited(retryAfter string) projectReply {
	return projectReply{429, `{"detail":{"message":"slow down","code":"RATE_LIMITED"}}`, map[string]string{"Retry-After": retryAfter}}
}

var meReply = projectReply{200, `{"id":"u1","username":"me"}`, nil}

// A 429 whose Retry-After is over the cap comes back from the first request,
// with RetryAfter set, instead of being slept out.
func TestA429OverTheCapIsReturnedAtOnce(t *testing.T) {
	c, reqs := capServer(t, 60*time.Second, limited("3600"), meReply)
	// The deadline keeps a client that does sleep from hanging the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := c.GetMe(ctx)
	var rle *RateLimitError
	if !errors.As(err, &rle) || rle.RetryAfter != 3600 {
		t.Fatalf("GetMe = %T %v, want a *RateLimitError with RetryAfter 3600", err, err)
	}
	if len(*reqs) != 1 {
		t.Errorf("%d requests, want 1", len(*reqs))
	}
	if d := time.Since(start); d >= time.Second {
		t.Errorf("GetMe took %v: it waited", d)
	}
}

// A Retry-After at or under the cap is waited out, then the request is sent
// again.
func TestA429AtOrUnderTheCapIsWaitedOut(t *testing.T) {
	for _, max := range []time.Duration{time.Second, 60 * time.Second} {
		c, reqs := capServer(t, max, limited("1"), meReply)
		start := time.Now()
		if _, err := c.GetMe(context.Background()); err != nil {
			t.Fatalf("cap %v: GetMe = %v", max, err)
		}
		if len(*reqs) != 2 {
			t.Errorf("cap %v: %d requests, want 2", max, len(*reqs))
		}
		if d := time.Since(start); d < time.Second {
			t.Errorf("cap %v: GetMe took %v, want at least the 1s Retry-After", max, d)
		}
	}
}

// Zero means no cap: a RetryConfig built as a literal without the field keeps
// the behaviour it had before the field existed.
func TestAZeroCapMeansNoCap(t *testing.T) {
	c, reqs := capServer(t, 0, limited("1"), meReply)
	if _, err := c.GetMe(context.Background()); err != nil {
		t.Fatalf("GetMe = %v", err)
	}
	if len(*reqs) != 2 {
		t.Errorf("%d requests, want 2: with no cap a 1s Retry-After is waited out", len(*reqs))
	}
}

// The cap holds on the request sent after a token refresh too.
func TestTheCapHoldsAfterATokenRefresh(t *testing.T) {
	expired := projectReply{401, `{"detail":{"message":"token expired","code":"AUTH_INVALID_TOKEN"}}`, nil}
	c, reqs := capServer(t, 60*time.Second, expired, limited("3600"), meReply)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := c.GetMe(ctx)
	var rle *RateLimitError
	if !errors.As(err, &rle) || rle.RetryAfter != 3600 {
		t.Fatalf("GetMe = %T %v, want a *RateLimitError with RetryAfter 3600", err, err)
	}
	if len(*reqs) != 2 {
		t.Errorf("%d requests, want 2 (the 401, then the 429)", len(*reqs))
	}
	if d := time.Since(start); d >= time.Second {
		t.Errorf("GetMe took %v: it waited", d)
	}
}

// ChangeUsername still returns its 429 at once when the client has no cap.
func TestChangeUsernameReturnsA429AtOnceWithNoCap(t *testing.T) {
	c, reqs := capServer(t, 0, limited("1"), projectReply{200, `{"old_username":"a","new_username":"b","changed_at":"2026-11-16T15:04:05Z"}`, nil})
	_, err := c.ChangeUsername(context.Background(), "b")
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("ChangeUsername = %T %v, want a *RateLimitError", err, err)
	}
	if len(*reqs) != 1 {
		t.Errorf("%d requests, want 1", len(*reqs))
	}
}
