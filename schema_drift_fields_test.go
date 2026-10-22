package colony_test

import (
	"encoding/json"
	"testing"
	"time"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// Fields the server added between the 2026-09-07 and 2026-09-10 schema
// snapshots. Each is decoded both present and absent, because a field
// modelled with the wrong zero value reads as data the server never sent.

func TestPostHeldFields(t *testing.T) {
	var held colony.Post
	if err := json.Unmarshal([]byte(`{"id":"p1","held":true,"held_explanation":"awaiting review"}`), &held); err != nil {
		t.Fatal(err)
	}
	if !held.Held || held.HeldExplanation == nil || *held.HeldExplanation != "awaiting review" {
		t.Errorf("held fields not decoded: held=%v explanation=%v", held.Held, held.HeldExplanation)
	}

	var plain colony.Post
	if err := json.Unmarshal([]byte(`{"id":"p2"}`), &plain); err != nil {
		t.Fatal(err)
	}
	if plain.Held || plain.HeldExplanation != nil {
		t.Errorf("absent held fields must decode to the server's defaults (false, nil), got %v %v",
			plain.Held, plain.HeldExplanation)
	}
}

func TestNotificationMessageReferences(t *testing.T) {
	const conv, msg = "5fd54353-4a01-45ec-9133-13c75b48956a", "0e305ef8-392e-430f-9ee4-6c6869fccbdc"
	var dm colony.Notification
	body := `{"id":"n1","notification_type":"direct_message","conversation_id":"` + conv + `","message_id":"` + msg + `"}`
	if err := json.Unmarshal([]byte(body), &dm); err != nil {
		t.Fatal(err)
	}
	if dm.ConversationID == nil || *dm.ConversationID != conv || dm.MessageID == nil || *dm.MessageID != msg {
		t.Errorf("message references not decoded: %v %v", dm.ConversationID, dm.MessageID)
	}

	var reply colony.Notification
	if err := json.Unmarshal([]byte(`{"id":"n2","notification_type":"reply","conversation_id":null,"message_id":null}`), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ConversationID != nil || reply.MessageID != nil {
		t.Errorf("null message references must decode to nil, got %v %v", reply.ConversationID, reply.MessageID)
	}
}

// TestCursorFoundKeepsItsDefault is why CursorFound is a *bool. The server's
// default is true, so the three states must stay distinct: an explicit
// false, an explicit true, and a response that does not mention it at all.
func TestCursorFoundKeepsItsDefault(t *testing.T) {
	cases := []struct {
		body string
		want *bool
	}{
		{`{"messages":[],"has_more":false,"cursor_found":false}`, colony.Ptr(false)},
		{`{"messages":[],"has_more":false,"cursor_found":true}`, colony.Ptr(true)},
		{`{"messages":[],"has_more":false}`, nil},
	}
	for _, tc := range cases {
		var h colony.ConversationHistory
		if err := json.Unmarshal([]byte(tc.body), &h); err != nil {
			t.Fatal(err)
		}
		switch {
		case tc.want == nil && h.CursorFound != nil:
			t.Errorf("%s: an absent cursor_found must stay nil, got %v", tc.body, *h.CursorFound)
		case tc.want != nil && (h.CursorFound == nil || *h.CursorFound != *tc.want):
			t.Errorf("%s: want %v, got %v", tc.body, *tc.want, h.CursorFound)
		}
	}
}

// author_colony_role (platform 2026-10-02a): the author's standing in the
// colony, on posts and comments. Null for a member and for a colony-less
// post, absent from an older platform; both must decode as nil, never "".
func TestAuthorColonyRole(t *testing.T) {
	var post colony.Post
	if err := json.Unmarshal([]byte(`{"id":"p1","author_colony_role":"founder"}`), &post); err != nil {
		t.Fatal(err)
	}
	if post.AuthorColonyRole == nil || *post.AuthorColonyRole != "founder" {
		t.Errorf("post role not decoded: %v", post.AuthorColonyRole)
	}
	if _, inExtra := post.Extra["author_colony_role"]; inExtra {
		t.Error("a modelled field must not also land in Extra")
	}
	var comment colony.Comment
	if err := json.Unmarshal([]byte(`{"id":"c1","author_colony_role":"moderator"}`), &comment); err != nil {
		t.Fatal(err)
	}
	if comment.AuthorColonyRole == nil || *comment.AuthorColonyRole != "moderator" {
		t.Errorf("comment role not decoded: %v", comment.AuthorColonyRole)
	}
	for _, body := range []string{`{"id":"p2","author_colony_role":null}`, `{"id":"p3"}`} {
		var p colony.Post
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		if p.AuthorColonyRole != nil {
			t.Errorf("%s: want nil, got %q", body, *p.AuthorColonyRole)
		}
	}
}

// edited_at and revision_count (platform 2026-10-22): when the title or body
// last changed, and how many earlier versions are kept. A post never edited
// sends null and 0; an older platform sends neither. Both must read as "not
// edited" — nil and 0 — never as a zero time.
func TestPostEditedAtAndRevisionCount(t *testing.T) {
	var edited colony.Post
	body := `{"id":"p1","edited_at":"2026-10-21T08:45:27.256805Z","revision_count":2,
	  "updated_at":"2026-10-22T09:00:00Z"}`
	if err := json.Unmarshal([]byte(body), &edited); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 21, 8, 45, 27, 256805000, time.UTC)
	if edited.EditedAt == nil || !edited.EditedAt.Equal(want) || edited.RevisionCount != 2 {
		t.Errorf("edited_at %v, revision_count %d", edited.EditedAt, edited.RevisionCount)
	}
	// A tag change moves updated_at and not edited_at; the two are separate
	// fields and must not be conflated.
	if edited.EditedAt.Equal(edited.UpdatedAt) {
		t.Error("EditedAt decoded from updated_at")
	}
	for _, key := range []string{"edited_at", "revision_count"} {
		if _, inExtra := edited.Extra[key]; inExtra {
			t.Errorf("modelled field %s also landed in Extra", key)
		}
	}

	for _, body := range []string{`{"id":"p2","edited_at":null,"revision_count":0}`, `{"id":"p3"}`} {
		var p colony.Post
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		if p.EditedAt != nil || p.RevisionCount != 0 {
			t.Errorf("%s: want nil and 0, got %v and %d", body, p.EditedAt, p.RevisionCount)
		}
	}
}

// trust_level (platform 2026-10-22): min_karma can be null, for the one rank
// with no floor, and max_karma is new, set only on the two negative ranks.
// MinKarma stays an int, so the null reads as 0; MaxKarma is what tells that
// rank apart from Newcomer's real floor of 0.
func TestTrustLevelNegativeRanks(t *testing.T) {
	cases := []struct {
		body    string
		wantMin int
		wantMax *int
	}{
		{`{"name":"Deep negative karma","min_karma":null,"max_karma":-50,"icon":"x","rate_multiplier":1.0}`, 0, colony.Ptr(-50)},
		{`{"name":"Negative karma","min_karma":-49,"max_karma":-10,"icon":"x","rate_multiplier":1.0}`, -49, colony.Ptr(-10)},
		{`{"name":"Newcomer","min_karma":0,"max_karma":null,"icon":"x","rate_multiplier":1.0}`, 0, nil},
		{`{"name":"Established","min_karma":200,"max_karma":null,"icon":"x","rate_multiplier":2.0}`, 200, nil},
		// A platform before 2026-10-22 sends no max_karma at all.
		{`{"name":"Veteran","min_karma":1000,"icon":"x","rate_multiplier":3.0}`, 1000, nil},
	}
	for _, tc := range cases {
		var u colony.User
		if err := json.Unmarshal([]byte(`{"id":"u1","trust_level":`+tc.body+`}`), &u); err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		tl := u.TrustLevel
		if tl == nil {
			t.Fatalf("%s: trust_level decoded as nil", tc.body)
		}
		if tl.MinKarma != tc.wantMin {
			t.Errorf("%s: MinKarma = %d, want %d", tc.body, tl.MinKarma, tc.wantMin)
		}
		switch {
		case tc.wantMax == nil && tl.MaxKarma != nil:
			t.Errorf("%s: MaxKarma = %d, want nil", tc.body, *tl.MaxKarma)
		case tc.wantMax != nil && (tl.MaxKarma == nil || *tl.MaxKarma != *tc.wantMax):
			t.Errorf("%s: MaxKarma = %v, want %d", tc.body, tl.MaxKarma, *tc.wantMax)
		}
	}
}

// Fields the server added between the 2026-10-02 snapshot and 2026-10-21.
// Each is decoded present and absent (or null), and none may also land in
// Extra once modelled.

func TestAdminEditAndImageNotice(t *testing.T) {
	var post colony.Post
	if err := json.Unmarshal([]byte(`{"id":"p1","admin_edited_at":"2026-10-21T08:00:00Z","image_notice":"1 image shown as text: host not listed"}`), &post); err != nil {
		t.Fatal(err)
	}
	if post.AdminEditedAt == nil || post.AdminEditedAt.UTC().Format("2006-01-02T15:04:05Z") != "2026-10-21T08:00:00Z" {
		t.Errorf("post admin_edited_at not decoded: %v", post.AdminEditedAt)
	}
	if post.ImageNotice == nil || *post.ImageNotice != "1 image shown as text: host not listed" {
		t.Errorf("post image_notice not decoded: %v", post.ImageNotice)
	}
	for _, k := range []string{"admin_edited_at", "image_notice"} {
		if _, inExtra := post.Extra[k]; inExtra {
			t.Errorf("modelled field %s must not also land in Extra", k)
		}
	}

	var comment colony.Comment
	if err := json.Unmarshal([]byte(`{"id":"c1","admin_edited_at":"2026-10-21T08:00:00Z","image_notice":"shown as text"}`), &comment); err != nil {
		t.Fatal(err)
	}
	if comment.AdminEditedAt == nil || comment.ImageNotice == nil || *comment.ImageNotice != "shown as text" {
		t.Errorf("comment fields not decoded: %v %v", comment.AdminEditedAt, comment.ImageNotice)
	}

	for _, body := range []string{`{"id":"p2","admin_edited_at":null,"image_notice":null}`, `{"id":"p3"}`} {
		var p colony.Post
		var c colony.Comment
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(body), &c); err != nil {
			t.Fatal(err)
		}
		if p.AdminEditedAt != nil || p.ImageNotice != nil || c.AdminEditedAt != nil || c.ImageNotice != nil {
			t.Errorf("%s: want all nil", body)
		}
	}
}

func TestWikiRedirectAndRevisionSizes(t *testing.T) {
	var page colony.WikiPage
	if err := json.Unmarshal([]byte(`{"id":"w1","slug":"new-name","redirected_from":"old-name"}`), &page); err != nil {
		t.Fatal(err)
	}
	if page.RedirectedFrom == nil || *page.RedirectedFrom != "old-name" || page.Slug != "new-name" {
		t.Errorf("redirected_from not decoded: %v (slug %q)", page.RedirectedFrom, page.Slug)
	}
	var current colony.WikiPage
	if err := json.Unmarshal([]byte(`{"id":"w2","slug":"new-name"}`), &current); err != nil {
		t.Fatal(err)
	}
	if current.RedirectedFrom != nil {
		t.Errorf("a page read by its current slug must have no redirect, got %q", *current.RedirectedFrom)
	}

	var rows []colony.WikiRevisionListItem
	if err := json.Unmarshal([]byte(`[{"id":"r2","size_bytes":120,"size_delta_bytes":-4800},{"id":"r1","size_bytes":4920,"size_delta_bytes":null},{"id":"r0"}]`), &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].SizeBytes != 120 || rows[0].SizeDeltaBytes == nil || *rows[0].SizeDeltaBytes != -4800 {
		t.Errorf("a blanking revision must keep its negative delta: %+v", rows[0])
	}
	if rows[1].SizeBytes != 4920 || rows[1].SizeDeltaBytes != nil {
		t.Errorf("a first revision has no delta (null), got %+v", rows[1])
	}
	if rows[2].SizeBytes != 0 || rows[2].SizeDeltaBytes != nil {
		t.Errorf("absent sizes must decode to the server's defaults (0, nil), got %+v", rows[2])
	}
	for _, k := range []string{"size_bytes", "size_delta_bytes"} {
		if _, inExtra := rows[0].Extra[k]; inExtra {
			t.Errorf("modelled field %s must not also land in Extra", k)
		}
	}
}

func TestModQueueItemPeople(t *testing.T) {
	var item colony.ModQueueItem
	body := `{"source_kind":"report","source_id":"s1","target_kind":"comment","target_id":"t1",
		"author":{"id":"u1","username":"writer","display_name":"Writer"},
		"reporter":{"id":"u2","username":"flagger","display_name":"Flagger","extra_key":1},
		"post_id":"p1","post_title":"A post","text":"the comment text","text_truncated":true}`
	if err := json.Unmarshal([]byte(body), &item); err != nil {
		t.Fatal(err)
	}
	if item.Author == nil || item.Author.Username != "writer" || item.Author.DisplayName != "Writer" || item.Author.ID != "u1" {
		t.Errorf("author not decoded: %+v", item.Author)
	}
	if item.Reporter == nil || item.Reporter.Username != "flagger" || item.Reporter.Extra["extra_key"] == nil {
		t.Errorf("reporter not decoded, or its unmodelled key lost: %+v", item.Reporter)
	}
	if item.PostID == nil || *item.PostID != "p1" || item.PostTitle == nil || *item.PostTitle != "A post" {
		t.Errorf("post fields not decoded: %v %v", item.PostID, item.PostTitle)
	}
	if item.Text != "the comment text" || !item.TextTruncated {
		t.Errorf("text fields not decoded: %q %v", item.Text, item.TextTruncated)
	}
	for _, k := range []string{"author", "reporter", "post_id", "post_title", "text", "text_truncated"} {
		if _, inExtra := item.Extra[k]; inExtra {
			t.Errorf("modelled field %s must not also land in Extra", k)
		}
	}

	var bare colony.ModQueueItem
	if err := json.Unmarshal([]byte(`{"source_kind":"automod","author":null,"reporter":null,"post_id":null,"post_title":null}`), &bare); err != nil {
		t.Fatal(err)
	}
	if bare.Author != nil || bare.Reporter != nil || bare.PostID != nil || bare.PostTitle != nil || bare.Text != "" || bare.TextTruncated {
		t.Errorf("null and absent fields must decode to the server's defaults, got %+v", bare)
	}
}
