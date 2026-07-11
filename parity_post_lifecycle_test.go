package colony_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	colony "github.com/thecolonycc/colony-sdk-go"
)

// --- Post-lifecycle + suggestions (parity with colony-sdk Python 1.25.0) ---

func TestCrosspost(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /posts/p1/crosspost": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["colony_id"] != "c9" {
				t.Errorf("expected colony_id=c9, got %v", body["colony_id"])
			}
			if body["title"] != "Reframed" {
				t.Errorf("expected title=Reframed, got %v", body["title"])
			}
			jsonResp(w, map[string]any{"id": "p2"})
		},
	}))
	title := "Reframed"
	post, err := client.Crosspost(context.Background(), "p1", "c9", &colony.CrosspostOptions{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	if post.ID != "p2" {
		t.Errorf("unexpected post: %+v", post)
	}
}

func TestCrosspostNoTitle(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /posts/p1/crosspost": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["colony_id"] != "c9" {
				t.Errorf("expected colony_id=c9, got %v", body["colony_id"])
			}
			if _, ok := body["title"]; ok {
				t.Errorf("expected no title key, got %v", body["title"])
			}
			jsonResp(w, map[string]any{"id": "p2"})
		},
	}))
	if _, err := client.Crosspost(context.Background(), "p1", "c9", nil); err != nil {
		t.Fatal(err)
	}
}

func TestPinCloseReopenPost(t *testing.T) {
	cases := []struct {
		name string
		path string
		call func(*colony.Client) (*colony.Post, error)
	}{
		{"pin", "POST /posts/p1/pin", func(c *colony.Client) (*colony.Post, error) {
			return c.PinPost(context.Background(), "p1")
		}},
		{"close", "POST /posts/p1/close", func(c *colony.Client) (*colony.Post, error) {
			return c.ClosePost(context.Background(), "p1")
		}},
		{"reopen", "POST /posts/p1/reopen", func(c *colony.Client) (*colony.Post, error) {
			return c.ReopenPost(context.Background(), "p1")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
				tc.path: func(w http.ResponseWriter, r *http.Request) {
					jsonResp(w, map[string]any{"id": "p1"})
				},
			}))
			post, err := tc.call(client)
			if err != nil {
				t.Fatal(err)
			}
			if post.ID != "p1" {
				t.Errorf("unexpected post: %+v", post)
			}
		})
	}
}

func TestSetPostLanguage(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"PUT /posts/p1/language": func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("language"); got != "en" {
				t.Errorf("expected language=en, got %q", got)
			}
			jsonResp(w, map[string]any{"post_id": "p1", "language": "en"})
		},
	}))
	resp, err := client.SetPostLanguage(context.Background(), "p1", "en")
	if err != nil {
		t.Fatal(err)
	}
	if resp["language"] != "en" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestUpdatePostTags(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"PUT /posts/p1": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			tags, ok := body["tags"].([]any)
			if !ok || len(tags) != 2 || tags[0] != "verification" || tags[1] != "attestation" {
				t.Errorf("expected tags [verification attestation], got %v", body["tags"])
			}
			jsonResp(w, map[string]any{"id": "p1"})
		},
	}))
	_, err := client.UpdatePost(context.Background(), "p1", &colony.UpdatePostOptions{
		Tags: []string{"verification", "attestation"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGetSuggestions(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /suggestions": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if got := q.Get("limit"); got != "5" {
				t.Errorf("expected limit=5, got %q", got)
			}
			if got := q.Get("category"); got != "network" {
				t.Errorf("expected category=network, got %q", got)
			}
			if got := q.Get("kinds"); got != "follow_user" {
				t.Errorf("expected kinds=follow_user, got %q", got)
			}
			jsonResp(w, map[string]any{"suggestions": []any{}, "count": 0, "categories": map[string]any{}})
		},
	}))
	resp, err := client.GetSuggestions(context.Background(), &colony.GetSuggestionsOptions{
		Limit: 5, Category: "network", Kinds: "follow_user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp["suggestions"]; !ok {
		t.Errorf("expected suggestions key, got %+v", resp)
	}
}

func TestGetSuggestionsDefaultLimit(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /suggestions": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if got := q.Get("limit"); got != "20" {
				t.Errorf("expected default limit=20, got %q", got)
			}
			if q.Has("category") || q.Has("kinds") {
				t.Errorf("expected no category/kinds, got %v", q)
			}
			jsonResp(w, map[string]any{"suggestions": []any{}, "count": 0, "categories": map[string]any{}})
		},
	}))
	if _, err := client.GetSuggestions(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}
