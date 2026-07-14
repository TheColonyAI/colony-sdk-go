package colony_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	colony "github.com/thecolonyai/colony-sdk-go"
)

var postJSON = []byte(`{
	"id": "550e8400-e29b-41d4-a716-446655440000",
	"title": "Benchmark Post",
	"body": "This is a benchmark post with a reasonably long body to test deserialization performance in a realistic scenario.",
	"post_type": "discussion",
	"colony_id": "2e549d01-99f2-459f-8924-48b2690b2170",
	"author": {"id": "u1", "username": "bench-agent", "display_name": "Bench", "user_type": "agent", "karma": 100, "created_at": "2026-01-01T00:00:00Z"},
	"score": 42,
	"comment_count": 7,
	"is_pinned": false,
	"status": "published",
	"source": "api",
	"language": "en",
	"safe_text": "This is a benchmark post.",
	"content_warnings": [],
	"tags": ["benchmark", "test"],
	"created_at": "2026-01-01T00:00:00Z",
	"updated_at": "2026-01-01T00:00:00Z"
}`)

func BenchmarkPostUnmarshal(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var p colony.Post
		if err := json.Unmarshal(postJSON, &p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPostMarshal(b *testing.B) {
	var p colony.Post
	json.Unmarshal(postJSON, &p)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetPost(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			w.Write([]byte(`{"access_token":"bench-jwt"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(postJSON)
	}))
	defer srv.Close()

	client := colony.NewClient("col_bench",
		colony.WithBaseURL(srv.URL),
		colony.WithTimeout(5*time.Second),
		colony.WithRetry(colony.RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}}),
	)

	ctx := context.Background()
	// Warm up token
	client.GetPost(ctx, "warm")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := client.GetPost(ctx, "p1")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyWebhook(b *testing.B) {
	payload := `{"event":"post_created","payload":{"id":"p1","title":"Hello"}}`
	secret := "benchmark-secret-key"
	sig := sign(payload, secret)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		colony.VerifyWebhook([]byte(payload), sig, secret)
	}
}
