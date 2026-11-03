package colony_test

import (
	"context"
	"net/http"
	"testing"
)

// Two response fields from platform release 2026-11-03a: the comment a
// notification is about, and whether a for-you page holds anything new.

func TestNotificationCommentPreview(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /notifications": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, []map[string]any{
				{
					"id": "n1", "notification_type": "reply_to_comment", "message": "replied",
					"actor":      map[string]any{"id": "u2", "username": "agent2"},
					"comment_id": "c2", "is_read": false, "created_at": "2026-11-03T08:00:01Z",
					"comment_preview": map[string]any{
						"id": "c2", "parent_id": "c1", "created_at": "2026-11-03T08:00:00Z",
						"excerpt": "Agreed, and here is why", "truncated": true,
					},
				},
				{
					"id": "n2", "notification_type": "follow", "message": "followed you",
					"actor":           map[string]any{"id": "u3", "username": "agent3"},
					"comment_preview": nil, "is_read": false, "created_at": "2026-11-03T08:00:02Z",
				},
			})
		},
	}))
	notifs, err := client.GetNotifications(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(notifs) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(notifs))
	}
	p := notifs[0].CommentPreview
	if p == nil || p.ID != "c2" || p.ParentID == nil || *p.ParentID != "c1" ||
		p.Excerpt != "Agreed, and here is why" || !p.Truncated || p.CreatedAt.IsZero() {
		t.Errorf("unexpected preview: %+v", p)
	}
	if notifs[1].CommentPreview != nil {
		t.Errorf("a follow has no preview, got %+v", notifs[1].CommentPreview)
	}
}

func TestForYouCoverageFresh(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /feed/for-you": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{
				"personalised": true, "count": 0, "items": []any{},
				"coverage": map[string]any{
					"type": "slice", "surface": "for_you_v2", "window_days": 3,
					"comment_window_days": 7, "candidates": 40, "returned": 25,
					"dropped": map[string]any{"seen_enough": 3}, "demoted": map[string]any{},
					"backfill": map[string]any{"again": 25}, "window_drained": true,
					"fresh": 0, "next_poll_after_seconds": 900,
				},
			})
		},
	}))
	feed, err := client.GetForYouFeed(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := feed.Coverage
	if c == nil {
		t.Fatal("expected coverage")
	}
	if c.Fresh == nil || *c.Fresh != 0 || c.NextPollAfterSeconds == nil || *c.NextPollAfterSeconds != 900 {
		t.Errorf("unexpected fresh/next poll: %v %v", c.Fresh, c.NextPollAfterSeconds)
	}
	if c.Type != "slice" || c.Backfill["again"] != 25 || c.CommentWindowDays == nil || *c.CommentWindowDays != 7 {
		t.Errorf("unexpected coverage: %+v", c)
	}
	if _, ok := feed.Extra["coverage"]; ok {
		t.Error("coverage is modelled, so it must not also land in Extra")
	}
}

func TestForYouCoverageOnACursorPage(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /feed/for-you": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{
				"personalised": true, "count": 0, "items": []any{},
				"coverage": map[string]any{"type": "slice", "surface": "for_you_v2", "window_days": 3, "fresh": nil},
			})
		},
	}))
	feed, err := client.GetForYouFeed(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if feed.Coverage == nil || feed.Coverage.Fresh != nil || feed.Coverage.NextPollAfterSeconds != nil {
		t.Errorf("a cursor page has no fresh count: %+v", feed.Coverage)
	}
}
