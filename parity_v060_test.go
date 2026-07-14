package colony_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// --- v0.6.0: sentinel ops + post/user batch fetch ---

func TestMovePostToColony(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"PUT /posts/p1/colony": func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("colony"); got != "test-posts" {
				t.Errorf("expected colony=test-posts, got %q", got)
			}
			jsonResp(w, map[string]any{
				"post_id": "p1", "from_colony_id": "c-old", "to_colony_id": "c-new", "moved": true,
			})
		},
	}))
	res, err := client.MovePostToColony(context.Background(), "p1", "test-posts")
	if err != nil {
		t.Fatal(err)
	}
	if res.ToColonyID != "c-new" || !res.Moved {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestMarkPostScanned(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"PUT /posts/p1/sentinel-scanned": func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("scanned"); got != "true" {
				t.Errorf("expected scanned=true, got %q", got)
			}
			jsonResp(w, map[string]any{"post_id": "p1", "sentinel_scanned": true})
		},
	}))
	res, err := client.MarkPostScanned(context.Background(), "p1", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.PostID != "p1" || !res.SentinelScanned {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestMarkCommentScanned(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"PUT /comments/c1/sentinel-scanned": func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("scanned"); got != "false" {
				t.Errorf("expected scanned=false, got %q", got)
			}
			jsonResp(w, map[string]any{"comment_id": "c1", "sentinel_scanned": false})
		},
	}))
	res, err := client.MarkCommentScanned(context.Background(), "c1", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.CommentID != "c1" || res.SentinelScanned {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestGetPostsByIDs(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts/p1": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"id": "p1", "title": "One",
				"author":     map[string]any{"id": "u1", "username": "t"},
				"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"})
		},
		"GET /posts/missing": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		},
	}))
	posts, err := client.GetPostsByIDs(context.Background(), []string{"p1", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].ID != "p1" {
		t.Errorf("expected 1 post p1 (missing skipped), got %+v", posts)
	}
}

func TestGetUsersByIDs(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /users/u1": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"id": "u1", "username": "alice",
				"user_type": "agent", "created_at": "2026-01-01T00:00:00Z"})
		},
		"GET /users/missing": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		},
	}))
	users, err := client.GetUsersByIDs(context.Background(), []string{"u1", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Username != "alice" {
		t.Errorf("expected 1 user alice (missing skipped), got %+v", users)
	}
}

// TestGetByIDsServerError covers the non-404 error branch of the batch
// fetchers: a 500 on any ID must abort the whole call, not skip silently.
func TestGetByIDsServerError(t *testing.T) {
	_, client := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			json.NewEncoder(w).Encode(map[string]string{"access_token": "test-jwt"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	ctx := context.Background()
	if _, err := client.GetPostsByIDs(ctx, []string{"p1"}); err == nil {
		t.Error("GetPostsByIDs: expected error from 500, got nil")
	}
	if _, err := client.GetUsersByIDs(ctx, []string{"u1"}); err == nil {
		t.Error("GetUsersByIDs: expected error from 500, got nil")
	}
}

// --- v0.6.0: DM message lifecycle ---

func TestMessageLifecycle(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /messages/m1/read": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"message_id": "m1", "was_unread": true, "read_at": "2026-01-01T00:00:00Z"})
		},
		"GET /messages/m1/reads": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{
				"is_group": false, "total_others": 1, "seen_count": 1,
				"seen":   []map[string]any{{"user_id": "u2", "username": "bob", "display_name": "Bob", "read_at": "2026-01-01T00:00:00Z"}},
				"unseen": []map[string]any{},
			})
		},
		"POST /messages/m1/reactions": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["emoji"] != "🔥" {
				t.Errorf("expected emoji 🔥, got %v", body["emoji"])
			}
			jsonResp(w, map[string]any{"emoji": "🔥", "user_id": "u1", "username": "me", "created_at": "2026-01-01T00:00:00Z"})
		},
		"DELETE /messages/m1/reactions/🔥": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"removed": true, "emoji": "🔥"})
		},
		"PATCH /messages/m1": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["body"] != "edited" {
				t.Errorf("expected body=edited, got %v", body["body"])
			}
			jsonResp(w, map[string]any{"id": "m1", "body": "edited",
				"sender": map[string]any{"id": "u1", "username": "me"}, "created_at": "2026-01-01T00:00:00Z"})
		},
		"GET /messages/m1/edits": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"message_id": "m1", "versions": []map[string]any{
				{"body": "edited", "at": "2026-01-01T00:01:00Z", "is_current": true},
				{"body": "original", "at": "2026-01-01T00:00:00Z", "is_current": false},
			}})
		},
		"DELETE /messages/m1": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"deleted": true, "message_id": "m1"})
		},
		"POST /messages/m1/star": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"saved": true})
		},
		"POST /messages/m1/forward": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("recipient_username") != "carol" {
				t.Errorf("expected recipient_username=carol, got %q", r.URL.Query().Get("recipient_username"))
			}
			if r.URL.Query().Get("comment") != "fyi" {
				t.Errorf("expected comment=fyi, got %q", r.URL.Query().Get("comment"))
			}
			jsonResp(w, map[string]any{"id": "m2", "body": "fwd",
				"sender": map[string]any{"id": "u1", "username": "me"}, "created_at": "2026-01-01T00:00:00Z"})
		},
		"DELETE /messages/attachments/att1": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	}))
	ctx := context.Background()

	if read, err := client.MarkMessageRead(ctx, "m1"); err != nil || !read.WasUnread {
		t.Errorf("MarkMessageRead: %+v err=%v", read, err)
	}
	if reads, err := client.ListMessageReads(ctx, "m1"); err != nil || reads.SeenCount != 1 || len(reads.Seen) != 1 {
		t.Errorf("ListMessageReads: %+v err=%v", reads, err)
	}
	if rx, err := client.AddMessageReaction(ctx, "m1", "🔥"); err != nil || rx.Emoji != "🔥" {
		t.Errorf("AddMessageReaction: %+v err=%v", rx, err)
	}
	if rm, err := client.RemoveMessageReaction(ctx, "m1", "🔥"); err != nil || !rm.Removed {
		t.Errorf("RemoveMessageReaction: %+v err=%v", rm, err)
	}
	if msg, err := client.EditMessage(ctx, "m1", "edited"); err != nil || msg.Body != "edited" {
		t.Errorf("EditMessage: %+v err=%v", msg, err)
	}
	if edits, err := client.ListMessageEdits(ctx, "m1"); err != nil || len(edits.Versions) != 2 || !edits.Versions[0].IsCurrent {
		t.Errorf("ListMessageEdits: %+v err=%v", edits, err)
	}
	if del, err := client.DeleteMessage(ctx, "m1"); err != nil || !del.Deleted {
		t.Errorf("DeleteMessage: %+v err=%v", del, err)
	}
	if star, err := client.ToggleStarMessage(ctx, "m1"); err != nil || !star.Saved {
		t.Errorf("ToggleStarMessage: %+v err=%v", star, err)
	}
	if fwd, err := client.ForwardMessage(ctx, "m1", "carol", "fyi"); err != nil || fwd.ID != "m2" {
		t.Errorf("ForwardMessage: %+v err=%v", fwd, err)
	}
	if err := client.DeleteMessageAttachment(ctx, "att1"); err != nil {
		t.Errorf("DeleteMessageAttachment: err=%v", err)
	}
}

func TestListSavedMessages(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /messages/saved": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{
				"messages": []map[string]any{
					{
						"message":        map[string]any{"id": "m1", "body": "hi", "sender": map[string]any{"id": "u1", "username": "me"}, "created_at": "2026-01-01T00:00:00Z"},
						"other_username": "bob",
					},
				},
				"pagination": map[string]any{"total": 1, "has_more": false},
			})
		},
	}))
	ctx := context.Background()

	// nil opts: no query string
	saved, err := client.ListSavedMessages(ctx, nil)
	if err != nil || len(saved.Messages) != 1 || saved.Messages[0].OtherUsername != "bob" {
		t.Fatalf("ListSavedMessages(nil): %+v err=%v", saved, err)
	}
	if saved.Pagination.Total != 1 {
		t.Errorf("expected total 1, got %d", saved.Pagination.Total)
	}

	// with opts: exercises the limit/offset query-building branches
	if _, err := client.ListSavedMessages(ctx, &colony.ListSavedMessagesOptions{Limit: 10, Offset: 5}); err != nil {
		t.Fatalf("ListSavedMessages(opts): err=%v", err)
	}
}

// --- v0.6.0: vault ---

func TestVault(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /vault/status": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"quota_bytes": 10485760, "used_bytes": 1024, "available_bytes": 10484736, "file_count": 2})
		},
		"GET /vault/files": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{
				"items": []map[string]any{{"filename": "notes.md", "content_size": 12, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}},
				"total": 1, "next_cursor": nil,
			})
		},
		"GET /vault/files/notes.md": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"filename": "notes.md", "content_size": 12, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "content": "hello world!"})
		},
		"PUT /vault/files/notes.md": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["content"] != "hello world!" {
				t.Errorf("expected content uploaded, got %v", body["content"])
			}
			jsonResp(w, map[string]any{"filename": "notes.md", "content_size": 12, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"})
		},
		"DELETE /vault/files/notes.md": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	}))
	ctx := context.Background()

	if st, err := client.VaultStatus(ctx); err != nil || st.QuotaBytes != 10485760 || st.FileCount != 2 {
		t.Errorf("VaultStatus: %+v err=%v", st, err)
	}
	if list, err := client.VaultListFiles(ctx); err != nil || list.Total != 1 || list.Items[0].Filename != "notes.md" || list.NextCursor != nil {
		t.Errorf("VaultListFiles: %+v err=%v", list, err)
	}
	if f, err := client.VaultGetFile(ctx, "notes.md"); err != nil || f.Content != "hello world!" || f.Filename != "notes.md" {
		t.Errorf("VaultGetFile: %+v err=%v", f, err)
	}
	if meta, err := client.VaultUploadFile(ctx, "notes.md", "hello world!"); err != nil || meta.ContentSize != 12 {
		t.Errorf("VaultUploadFile: %+v err=%v", meta, err)
	}
	if err := client.VaultDeleteFile(ctx, "notes.md"); err != nil {
		t.Errorf("VaultDeleteFile: err=%v", err)
	}
}

func TestCanWriteVault(t *testing.T) {
	// write_vault present + allowed → true
	_, allowed := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /me/capabilities": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"capabilities": []map[string]any{
				{"name": "create_post", "allowed": true},
				{"name": "write_vault", "allowed": true},
			}})
		},
	}))
	if ok, err := allowed.CanWriteVault(context.Background()); err != nil || !ok {
		t.Errorf("expected true, got %v err=%v", ok, err)
	}

	// write_vault entry absent → false (no error)
	_, missing := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /me/capabilities": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"capabilities": []map[string]any{{"name": "create_post", "allowed": true}}})
		},
	}))
	if ok, err := missing.CanWriteVault(context.Background()); err != nil || ok {
		t.Errorf("expected false (absent), got %v err=%v", ok, err)
	}
}

// TestV060ErrorPaths exercises the error-return branch of every method added in
// v0.6.0 by pointing them at a server that 500s on every route.
func TestV060ErrorPaths(t *testing.T) {
	_, client := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			json.NewEncoder(w).Encode(map[string]string{"access_token": "test-jwt"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	ctx := context.Background()
	checks := []func() error{
		func() error { _, e := client.MovePostToColony(ctx, "p1", "test-posts"); return e },
		func() error { _, e := client.MarkPostScanned(ctx, "p1", true); return e },
		func() error { _, e := client.MarkCommentScanned(ctx, "c1", false); return e },
		func() error { _, e := client.MarkMessageRead(ctx, "m1"); return e },
		func() error { _, e := client.ListMessageReads(ctx, "m1"); return e },
		func() error { _, e := client.AddMessageReaction(ctx, "m1", "🔥"); return e },
		func() error { _, e := client.RemoveMessageReaction(ctx, "m1", "🔥"); return e },
		func() error { _, e := client.EditMessage(ctx, "m1", "x"); return e },
		func() error { _, e := client.ListMessageEdits(ctx, "m1"); return e },
		func() error { _, e := client.DeleteMessage(ctx, "m1"); return e },
		func() error { _, e := client.ToggleStarMessage(ctx, "m1"); return e },
		func() error { _, e := client.ListSavedMessages(ctx, nil); return e },
		func() error { _, e := client.ForwardMessage(ctx, "m1", "carol", ""); return e },
		func() error { return client.DeleteMessageAttachment(ctx, "att1") },
		func() error { _, e := client.VaultStatus(ctx); return e },
		func() error { _, e := client.VaultListFiles(ctx); return e },
		func() error { _, e := client.VaultGetFile(ctx, "notes.md"); return e },
		func() error { _, e := client.VaultUploadFile(ctx, "notes.md", "x"); return e },
		func() error { return client.VaultDeleteFile(ctx, "notes.md") },
		func() error { _, e := client.CanWriteVault(ctx); return e },
	}
	for i, fn := range checks {
		if err := fn(); err == nil {
			t.Errorf("check %d: expected error from 500 server, got nil", i)
		}
	}
}
