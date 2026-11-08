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
	headers := map[string]string{}
	if header != "" {
		headers[ignoredParamsHeader] = header
	}
	return ignoredServerWith(t, headers)
}

// ignoredServerWith answers every request with an empty list and the given
// response headers.
func ignoredServerWith(t *testing.T, headers map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		for k, v := range headers {
			w.Header().Set(k, v)
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

// The body twin (platform release 2026-11-08a): X-Colony-Ignored-Fields names
// the JSON body's top-level keys the route ignored, at most ten, with "..."
// for the rest. Each is logged once per route and name, "..." included, and
// the message says it was a body field.
func TestIgnoredFieldsWarnOncePerRouteAndName(t *testing.T) {
	var logs bytes.Buffer
	url := ignoredServerWith(t, map[string]string{ignoredFieldsHeader: "colony, audio_id, ..."})
	c := NewClient("col_x", WithBaseURL(url),
		WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))))
	ctx := context.Background()
	for _, id := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		if _, err := c.SetPostAudio(ctx, id, audioID); err != nil {
			t.Fatal(err)
		}
	}
	out := logs.String()
	if n := strings.Count(out, "level=WARN"); n != 3 {
		t.Fatalf("want one warning per name (3), got %d:\n%s", n, out)
	}
	for _, want := range []string{"field=colony", "field=audio_id", "field=...",
		"route=\"PUT /posts/{id}/audio\"", "request body field"} {
		if !strings.Contains(out, want) {
			t.Errorf("warnings lack %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "query parameter") || strings.Contains(out, "param=") {
		t.Errorf("a body field was logged as a query parameter:\n%s", out)
	}
}

// One response can carry both headers, and a query parameter and a body field
// of the same name are different findings: each is logged, once.
func TestIgnoredParamsAndFieldsAreLoggedApart(t *testing.T) {
	var logs bytes.Buffer
	url := ignoredServerWith(t, map[string]string{ignoredParamsHeader: "colony", ignoredFieldsHeader: "colony"})
	c := NewClient("col_x", WithBaseURL(url),
		WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))))
	for i := 0; i < 2; i++ {
		if _, err := c.RemovePostAudio(context.Background(), "11111111-1111-4111-8111-111111111111"); err != nil {
			t.Fatal(err)
		}
	}
	out := logs.String()
	if n := strings.Count(out, "level=WARN"); n != 2 {
		t.Fatalf("want the parameter and the field once each (2), got %d:\n%s", n, out)
	}
	for _, want := range []string{"param=colony", "field=colony", "query parameter", "request body field"} {
		if !strings.Contains(out, want) {
			t.Errorf("warnings lack %q:\n%s", want, out)
		}
	}
}

func TestIgnoredFieldsSilentWithoutALogger(t *testing.T) {
	c := NewClient("col_x", WithBaseURL(ignoredServerWith(t, map[string]string{ignoredFieldsHeader: "colony"})))
	if _, err := c.SetPostAudio(context.Background(), "11111111-1111-4111-8111-111111111111", audioID); err != nil {
		t.Fatal(err)
	}
	if n := len(c.ignoredWarned.seen); n != 0 {
		t.Errorf("recorded %d name(s) with no logger to tell", n)
	}
}
