package colony

import (
	"context"
	"strings"
	"testing"
)

// Message previews say when they were cut (platform release 2026-11-18b):
// each row of GET /messages/conversations gains last_message_preview_truncated
// beside last_message_preview (at most 120 characters, cut on a word, ending in
// "…" when cut), read into Conversation.LastMessagePreviewTruncated. An older
// server does not send it, which reads as false.

// conversationRowJSON is ConversationOut's own example in the server's OpenAPI
// document.
const conversationRowJSON = `{"id":"00000000-0000-0000-0000-000000000001",` +
	`"other_user":{"id":"00000000-0000-0000-0000-000000000003","username":"alice","display_name":"Alice",` +
	`"user_type":"agent","created_at":"2026-01-01T00:00:00Z"},` +
	`"last_message_at":"2026-05-26T11:00:00Z","unread_count":1,` +
	`"last_message_preview":"Hello — shipping the new build…","last_message_preview_truncated":true,` +
	`"is_archived":false}`

func TestListConversationsReadsThePreviewFlag(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, "[" + conversationRowJSON + "]", nil})
	got, err := c.ListConversations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 || (*reqs)[0].method != "GET" || (*reqs)[0].path != "/messages/conversations" {
		t.Fatalf("requests = %+v, want one GET /messages/conversations", *reqs)
	}
	if len(got) != 1 {
		t.Fatalf("got %d conversations, want 1", len(got))
	}
	row := got[0]
	if !row.LastMessagePreviewTruncated {
		t.Error("LastMessagePreviewTruncated = false, want true")
	}
	if !strings.HasSuffix(row.LastMessagePreview, "…") {
		t.Errorf("LastMessagePreview = %q, want it to end in …", row.LastMessagePreview)
	}
	if row.OtherUser.Username != "alice" {
		t.Errorf("OtherUser.Username = %q: the name GetConversation takes for the whole message", row.OtherUser.Username)
	}
}

func TestListConversationsFromAnOlderServer(t *testing.T) {
	old := strings.Replace(conversationRowJSON, `"last_message_preview_truncated":true,`, "", 1)
	if old == conversationRowJSON {
		t.Fatal("the fixture no longer carries the flag to remove")
	}
	c, _ := projectServer(t, projectReply{200, "[" + old + "]", nil})
	got, err := c.ListConversations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].LastMessagePreviewTruncated {
		t.Errorf("got %+v, want one row with LastMessagePreviewTruncated false", got)
	}
}
