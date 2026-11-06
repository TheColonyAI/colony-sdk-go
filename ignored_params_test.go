package colony

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ignoredServer answers every request with an empty list and the given
// X-Colony-Ignored-Params value (none when empty).
func ignoredServer(t *testing.T, header string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		if header != "" {
			w.Header().Set(ignoredParamsHeader, header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestIgnoredParamsWarnOncePerRouteAndName(t *testing.T) {
	var logs bytes.Buffer
	c := NewClient("col_x", WithBaseURL(ignoredServer(t, "skip, take")),
		WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))))
	ctx := context.Background()
	for _, id := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		if _, err := c.GetComments(ctx, id, 1); err != nil {
			t.Fatal(err)
		}
	}
	out := logs.String()
	if n := strings.Count(out, "level=WARN"); n != 2 {
		t.Fatalf("want one warning per name (2), got %d:\n%s", n, out)
	}
	for _, want := range []string{"param=skip", "param=take", "route=\"GET /posts/{id}/comments\""} {
		if !strings.Contains(out, want) {
			t.Errorf("warnings lack %q:\n%s", want, out)
		}
	}
}

func TestIgnoredParamsSilentWithoutALoggerOrAHeader(t *testing.T) {
	ctx := context.Background()
	quiet := NewClient("col_x", WithBaseURL(ignoredServer(t, "skip")))
	if _, err := quiet.GetComments(ctx, "11111111-1111-4111-8111-111111111111", 1); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	c := NewClient("col_x", WithBaseURL(ignoredServer(t, "")),
		WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))))
	if _, err := c.GetComments(ctx, "11111111-1111-4111-8111-111111111111", 1); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Errorf("logged without the header:\n%s", logs.String())
	}
}
