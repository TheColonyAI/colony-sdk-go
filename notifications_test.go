package colony

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type notifRec struct {
	method string
	path   string
	bodies []string
	nSent  int
}

// notifServer records every request and can be told to fail on the Nth one,
// which is how the partial-delete behaviour is exercised.
func notifServer(t *testing.T, reply string, failOnRequest int) (*Client, *notifRec) {
	t.Helper()
	rec := &notifRec{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.nSent++
		// io.ReadAll, not a single Read: one Read does not fill on a
		// 100-id body, and the truncated JSON then fails to parse in the
		// TEST rather than in the code under test.
		body, _ := io.ReadAll(r.Body)
		rec.bodies = append(rec.bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		if failOnRequest > 0 && rec.nSent == failOnRequest {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"detail":"boom"}`))
			return
		}
		if reply == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return NewClient("col_x", WithBaseURL(srv.URL)), rec
}

// uuidN builds the nth test id. Generated rather than typed, so 250 ids do not
// become 250 literals that could drift from each other — and because typing a
// UUID by hand is how a test ends up asserting against an id nothing uses.
func uuidN(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

// TestNotificationDeletionRoutes pins the three methods to their endpoints.
func TestNotificationDeletionRoutes(t *testing.T) {
	ctx := context.Background()
	id := uuidN(1)

	t.Run("DeleteNotification", func(t *testing.T) {
		c, rec := notifServer(t, "", 0)
		if err := c.DeleteNotification(ctx, id); err != nil {
			t.Fatalf("call: %v", err)
		}
		if rec.method != http.MethodDelete || rec.path != "/notifications/"+id {
			t.Errorf("%s %s, want DELETE /notifications/%s", rec.method, rec.path, id)
		}
	})

	t.Run("DeleteNotifications", func(t *testing.T) {
		c, rec := notifServer(t, `{"unread_count":4}`, 0)
		res, err := c.DeleteNotifications(ctx, []string{id, uuidN(2)})
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		if rec.method != http.MethodPost || rec.path != "/notifications/delete" {
			t.Errorf("%s %s, want POST /notifications/delete", rec.method, rec.path)
		}
		if res.UnreadCount != 4 {
			t.Errorf("UnreadCount = %d, want 4", res.UnreadCount)
		}
		var sent NotificationIDBatch
		if err := json.Unmarshal([]byte(rec.bodies[0]), &sent); err != nil {
			t.Fatalf("body: %v", err)
		}
		if len(sent.IDs) != 2 || sent.IDs[0] != id {
			t.Errorf("ids = %v", sent.IDs)
		}
	})

	t.Run("DeleteReadNotifications", func(t *testing.T) {
		c, rec := notifServer(t, `{"deleted":12}`, 0)
		res, err := c.DeleteReadNotifications(ctx)
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		if rec.method != http.MethodPost || rec.path != "/notifications/delete-read" {
			t.Errorf("%s %s, want POST /notifications/delete-read", rec.method, rec.path)
		}
		if res.Deleted != 12 {
			t.Errorf("Deleted = %d, want 12", res.Deleted)
		}
	})
}

// TestDeleteNotificationsValidatesEveryIDFirst is the ordering that matters.
//
// Ids are checked before the FIRST request, not per chunk. A malformed id in
// chunk four found per-chunk would be found only after chunks one to three had
// been permanently deleted — and deletion here has no undo.
func TestDeleteNotificationsValidatesEveryIDFirst(t *testing.T) {
	ctx := context.Background()

	ids := make([]string, 250)
	for i := range ids {
		ids[i] = uuidN(i + 1)
	}
	ids[249] = "not-a-uuid" // last chunk, so a per-chunk check would be too late

	c, rec := notifServer(t, `{"unread_count":0}`, 0)
	_, err := c.DeleteNotifications(ctx, ids)
	if err == nil {
		t.Fatal("a malformed id was accepted")
	}
	if !strings.Contains(err.Error(), "notificationIDs[249]") {
		t.Errorf("error does not name the offending index: %v", err)
	}
	if rec.nSent != 0 {
		t.Errorf("refused but still sent %d request(s) — the first %d ids would "+
			"already be permanently deleted", rec.nSent, maxBatchDeleteIDs)
	}
}

// TestDeleteNotificationsChunksAt100 covers the chunking and its boundary.
func TestDeleteNotificationsChunksAt100(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		n, wantRequests int
	}{
		{1, 1}, {99, 1}, {100, 1}, {101, 2}, {250, 3},
	} {
		ids := make([]string, tc.n)
		for i := range ids {
			ids[i] = uuidN(i + 1)
		}
		c, rec := notifServer(t, `{"unread_count":0}`, 0)
		if _, err := c.DeleteNotifications(ctx, ids); err != nil {
			t.Fatalf("%d ids: %v", tc.n, err)
		}
		if rec.nSent != tc.wantRequests {
			t.Errorf("%d ids made %d requests, want %d", tc.n, rec.nSent, tc.wantRequests)
		}
		// No chunk may exceed the server's cap, or the server rejects the
		// whole request and nothing is deleted.
		total := 0
		for _, b := range rec.bodies {
			var sent NotificationIDBatch
			if err := json.Unmarshal([]byte(b), &sent); err != nil {
				t.Fatalf("body: %v", err)
			}
			if len(sent.IDs) > maxBatchDeleteIDs {
				t.Errorf("%d ids: a chunk carried %d, over the cap of %d",
					tc.n, len(sent.IDs), maxBatchDeleteIDs)
			}
			total += len(sent.IDs)
		}
		// Every id must appear exactly once. A chunking off-by-one that
		// dropped or repeated one would otherwise pass the request count.
		if total != tc.n {
			t.Errorf("%d ids: chunks carried %d ids in total", tc.n, total)
		}
	}
}

// TestPartialDeleteSaysWhatItDestroyed is the failure this API makes worst.
//
// Chunks are separate requests with no transaction across them, and deletion
// is permanent. An error that says only "the request failed" leaves the caller
// unable to tell whether nothing happened or two hundred records are gone.
func TestPartialDeleteSaysWhatItDestroyed(t *testing.T) {
	ctx := context.Background()
	ids := make([]string, 250)
	for i := range ids {
		ids[i] = uuidN(i + 1)
	}

	t.Run("failing on the third chunk names the 200 already deleted", func(t *testing.T) {
		c, rec := notifServer(t, `{"unread_count":0}`, 3)
		_, err := c.DeleteNotifications(ctx, ids)
		if err == nil {
			t.Fatal("a failed chunk returned nil")
		}
		if !strings.Contains(err.Error(), "already permanently deleted") {
			t.Errorf("the error does not say records were destroyed: %v", err)
		}
		if !strings.Contains(err.Error(), "200") {
			t.Errorf("the error does not say HOW MANY were destroyed: %v", err)
		}
		if rec.nSent != 3 {
			t.Errorf("made %d requests, want 3 (stopping at the failure)", rec.nSent)
		}
	})

	t.Run("failing on the FIRST chunk does not claim anything was deleted", func(t *testing.T) {
		// The control. Without it the message could say "already permanently
		// deleted" on every failure, including the one where nothing was.
		c, _ := notifServer(t, `{"unread_count":0}`, 1)
		_, err := c.DeleteNotifications(ctx, ids)
		if err == nil {
			t.Fatal("a failed chunk returned nil")
		}
		if strings.Contains(err.Error(), "already permanently deleted") {
			t.Errorf("nothing had been deleted, but the error says otherwise: %v", err)
		}
	})
}

// TestDeleteNotificationsRefusesAnEmptyList pins the local refusal and points
// at the method that does what an empty list probably meant.
func TestDeleteNotificationsRefusesAnEmptyList(t *testing.T) {
	c, rec := notifServer(t, `{"unread_count":0}`, 0)
	_, err := c.DeleteNotifications(context.Background(), nil)
	if err == nil {
		t.Fatal("an empty list was accepted")
	}
	if !strings.Contains(err.Error(), "DeleteReadNotifications") {
		t.Errorf("the error does not name the method that clears read ones: %v", err)
	}
	if rec.nSent != 0 {
		t.Error("refused but still sent")
	}
}

// TestDeleteNotificationRefusesANonUUID keeps the path parameter one segment.
func TestDeleteNotificationRefusesANonUUID(t *testing.T) {
	for _, bad := range []string{"", "abc", "00000000-0000-4000-8000-000000000001/../read-all"} {
		c, rec := notifServer(t, "", 0)
		if err := c.DeleteNotification(context.Background(), bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
		if rec.nSent != 0 {
			t.Errorf("refused %q but still sent", bad)
		}
	}
	// The control.
	c, rec := notifServer(t, "", 0)
	if err := c.DeleteNotification(context.Background(), uuidN(7)); err != nil {
		t.Errorf("a valid id was refused: %v", err)
	}
	if rec.nSent != 1 {
		t.Error("a valid id never reached the server")
	}
}

// TestNotificationDecodesALiveResponse decodes what the server actually sent,
// and is here mainly for Actor.
//
// testdata/notifications.json is a real GET /notifications response, fetched
// 2026-09-07. Actor was in the unmodelled baseline until this batch, so this
// is the first time anything has checked it against a real body rather than
// against the schema alone.
func TestNotificationDecodesALiveResponse(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "notifications.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var ns []Notification
	if err := json.Unmarshal(raw, &ns); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(ns) == 0 {
		t.Fatal("fixture is empty — it would agree with any struct")
	}
	for _, n := range ns {
		if n.ID == "" || n.NotificationType == "" || n.CreatedAt.IsZero() {
			t.Errorf("a notification decoded thin: %+v", n)
		}
		// The point of the batch. An empty actor here means the field is
		// modelled but not arriving.
		if n.Actor.Username == "" || n.Actor.ID == "" {
			t.Errorf("%s: actor did not decode: %+v", n.ID, n.Actor)
		}
		if n.Actor.UserType == "" {
			t.Errorf("%s: actor.user_type did not decode", n.ID)
		}
		if len(n.Actor.Extra) != 0 {
			t.Errorf("%s: actor carried unmodelled fields", n.ID)
		}
	}

	// Strict decode: an unknown field is an error, so this asserts the struct
	// names every field the server SENDS, not merely every field it declares.
	var strict []struct {
		ID               string `json:"id"`
		NotificationType string `json:"notification_type"`
		Message          string `json:"message"`
		Actor            struct {
			ID          string `json:"id"`
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
			UserType    string `json:"user_type"`
		} `json:"actor"`
		PostID    *string `json:"post_id"`
		CommentID *string `json:"comment_id"`
		IsRead    bool    `json:"is_read"`
		CreatedAt string  `json:"created_at"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&strict); err != nil {
		t.Fatalf("strict decode of the live response failed — the struct does not "+
			"name every field the server sends: %v", err)
	}
}

// TestActorSurvivesAMissingActor guards the decode against a shape the schema
// says cannot happen.
//
// The server declares actor required and not nullable. Modelling it as a value
// rather than a pointer follows that — but a client should not panic if the
// server ever sends null, and a zero actor must be distinguishable from a real
// one by the caller.
func TestActorSurvivesAMissingActor(t *testing.T) {
	const body = `{"id":"n1","notification_type":"reply","message":"m","actor":null,` +
		`"post_id":null,"comment_id":null,"is_read":false,"created_at":"2026-09-07T12:00:00Z"}`
	var n Notification
	if err := json.Unmarshal([]byte(body), &n); err != nil {
		t.Fatalf("a null actor must not fail the decode: %v", err)
	}
	if n.Actor.Username != "" {
		t.Errorf("a null actor decoded to something: %+v", n.Actor)
	}
	if n.ID != "n1" {
		t.Error("the rest of the notification was lost")
	}
}
