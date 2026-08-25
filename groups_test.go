package colony

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// recorded is what the group stub server saw.
type recorded struct {
	method string
	path   string
	query  url.Values
	body   map[string]any
}

func groupServer(t *testing.T, reply string) (*Client, *recorded) {
	t.Helper()
	got := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		got.method, got.path, got.query = r.Method, r.URL.Path, r.URL.Query()
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &got.body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return NewClient("col_x", WithBaseURL(srv.URL)), got
}

// The one shape in this file verified against the live API, so it is pinned
// against the wire rather than against the Python docstring — which documents
// a `role_labels` field the server does not send and omits `pagination`.
func TestListGroupTemplatesDecodesTheLiveShape(t *testing.T) {
	body := `{"templates":[{"slug":"software-team","title":"Software Team",
	  "description":"Daily coordination for a coding team.",
	  "suggested_roles":["Coder — writes + edits code","Reviewer — code review + style"],
	  "starter_pinned_message":"Welcome to the software team."}],
	  "pagination":{"total":3}}`
	c, got := groupServer(t, body)

	list, err := c.ListGroupTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/messages/groups/templates" {
		t.Errorf("path = %s", got.path)
	}
	if len(list.Templates) != 1 {
		t.Fatalf("templates = %d", len(list.Templates))
	}
	tpl := list.Templates[0]
	if tpl.Slug != "software-team" || tpl.Title != "Software Team" {
		t.Errorf("template = %+v", tpl)
	}
	if len(tpl.SuggestedRoles) != 2 || !strings.HasPrefix(tpl.SuggestedRoles[0], "Coder") {
		t.Errorf("SuggestedRoles = %v", tpl.SuggestedRoles)
	}
	if tpl.StarterPinnedMessage == "" {
		t.Error("StarterPinnedMessage lost")
	}
	if list.Pagination["total"] != float64(3) {
		t.Errorf("pagination = %v", list.Pagination)
	}
}

// The insurance for every OTHER shape in this file, which is modelled from
// docstrings rather than the wire: a field this package models wrongly or not
// at all must still be reachable through Extra rather than silently dropped.
func TestUnmodelledGroupFieldsAreReachable(t *testing.T) {
	c, _ := groupServer(t, `{"id":"g1","title":"T",
	  "a_field_this_sdk_does_not_model":{"nested":1}}`)
	g, err := c.GetGroupConversation(context.Background(), "g1", nil)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := g.Extra["a_field_this_sdk_does_not_model"].(map[string]any)
	if !ok {
		t.Fatalf("unreachable: Extra = %#v", g.Extra)
	}
	if v["nested"] != float64(1) {
		t.Errorf("nested = %v", v["nested"])
	}
	// And the control: a fully-modelled response leaves Extra nil.
	c2, _ := groupServer(t, `{"id":"g1","title":"T"}`)
	g2, err := c2.GetGroupConversation(context.Background(), "g1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if g2.Extra != nil {
		t.Errorf("Extra = %#v, want nil", g2.Extra)
	}
}

func TestCreateGroupConversation(t *testing.T) {
	c, got := groupServer(t, `{"id":"g1","title":"Team","is_group":true,"creator_id":"u1",
	  "members":[{"id":"u2","username":"bob","display_name":"Bob"}]}`)

	g, err := c.CreateGroupConversation(context.Background(), "Team", []string{"bob", "carol"})
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/messages/groups" {
		t.Errorf("%s %s", got.method, got.path)
	}
	if got.query.Get("title") != "Team" {
		t.Errorf("title = %q", got.query.Get("title"))
	}
	// Repeated key, not a comma-joined string — the server reads members as a
	// multi-value parameter, and joining them makes one member named "bob,carol".
	if m := got.query["members"]; len(m) != 2 || m[0] != "bob" || m[1] != "carol" {
		t.Errorf("members = %v, want two separate values", m)
	}
	if g.ID != "g1" || !g.IsGroup || len(g.Members) != 1 {
		t.Errorf("group = %+v", g)
	}
}

func TestCreateGroupRefusesEmptyInput(t *testing.T) {
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/token" {
			sent = true
		}
		_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
	}))
	defer srv.Close()
	c := NewClient("col_x", WithBaseURL(srv.URL))
	ctx := context.Background()

	if _, err := c.CreateGroupConversation(ctx, "", []string{"bob"}); err == nil {
		t.Error("empty title accepted")
	}
	if _, err := c.CreateGroupConversation(ctx, "T", nil); err == nil {
		t.Error("empty member list accepted")
	}
	if _, err := c.CreateGroupFromTemplate(ctx, "", []string{"bob"}, nil); err == nil {
		t.Error("empty template slug accepted")
	}
	if _, err := c.SendGroupMessage(ctx, "g1", "   ", nil); err == nil {
		t.Error("whitespace-only message accepted")
	}
	if _, err := c.SnoozeGroupConversation(ctx, "g1", ""); err == nil {
		t.Error("empty snooze duration accepted")
	}
	if sent {
		t.Error("a request was sent for input the client should have refused")
	}
}

func TestCreateGroupFromTemplate(t *testing.T) {
	c, got := groupServer(t, `{"id":"g1","template":"software-team","starter_message_id":"m1"}`)
	title := "Override"
	g, err := c.CreateGroupFromTemplate(context.Background(), "software-team",
		[]string{"bob"}, &CreateGroupFromTemplateOptions{TitleOverride: title})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/messages/groups/from-template" {
		t.Errorf("path = %s", got.path)
	}
	if got.query.Get("template") != "software-team" || got.query.Get("title_override") != title {
		t.Errorf("query = %v", got.query)
	}
	if g.Template != "software-team" || g.StarterMessageID == nil || *g.StarterMessageID != "m1" {
		t.Errorf("group = %+v", g)
	}
}

func TestSendGroupMessage(t *testing.T) {
	c, got := groupServer(t, `{"id":"m1","body":"hi","conversation_id":"g1"}`)
	m, err := c.SendGroupMessage(context.Background(), "g1", "hi",
		&SendGroupMessageOptions{ReplyToMessageID: "m0"})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/messages/groups/g1/send" {
		t.Errorf("path = %s", got.path)
	}
	if got.body["body"] != "hi" || got.body["reply_to_message_id"] != "m0" {
		t.Errorf("body = %v", got.body)
	}
	if m.ID != "m1" {
		t.Errorf("message = %+v", m)
	}

	// Without the option, reply_to_message_id must be ABSENT rather than "".
	// An empty string is a message id the server would try to resolve.
	c2, got2 := groupServer(t, `{"id":"m2"}`)
	if _, err := c2.SendGroupMessage(context.Background(), "g1", "hi", nil); err != nil {
		t.Fatal(err)
	}
	if _, present := got2.body["reply_to_message_id"]; present {
		t.Errorf("reply_to_message_id sent as %v when no reply was intended", got2.body["reply_to_message_id"])
	}
}

// Optional query parameters must be omitted, not sent empty — an empty
// `until` or `show` is a different request from not setting one.
func TestOptionalParametersAreOmittedNotEmptied(t *testing.T) {
	ctx := context.Background()

	c, got := groupServer(t, `{"muted":true,"muted_until":null}`)
	st, err := c.MuteGroupConversation(ctx, "g1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.query) != 0 {
		t.Errorf("indefinite mute sent query %v, want none", got.query)
	}
	if !st.Muted || st.MutedUntil != nil {
		t.Errorf("state = %+v", st)
	}

	c, got = groupServer(t, `{"muted":true,"muted_until":"2026-09-01T00:00:00Z"}`)
	if _, err := c.MuteGroupConversation(ctx, "g1", "2026-09-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if got.query.Get("until") != "2026-09-01T00:00:00Z" {
		t.Errorf("until = %q", got.query.Get("until"))
	}

	c, got = groupServer(t, `{"override":null,"effective":true}`)
	rr, err := c.SetGroupReadReceipts(ctx, "g1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.query) != 0 {
		t.Errorf("clearing the override sent query %v, want none", got.query)
	}
	// nil Override means "no explicit setting", which is not the same as
	// false — the whole reason it is a pointer.
	if rr.Override != nil || !rr.Effective {
		t.Errorf("state = %+v", rr)
	}

	show := false
	c, got = groupServer(t, `{"override":false,"effective":false}`)
	rr, err = c.SetGroupReadReceipts(ctx, "g1", &show)
	if err != nil {
		t.Fatal(err)
	}
	if got.query.Get("show") != "false" {
		t.Errorf("show = %q", got.query.Get("show"))
	}
	if rr.Override == nil || *rr.Override {
		t.Errorf("Override = %v, want an explicit false", rr.Override)
	}

	// UpdateGroupConversation: nil fields must not appear at all, so that
	// clearing a description is distinguishable from leaving it alone.
	c, got = groupServer(t, `{"id":"g1"}`)
	if _, err := c.UpdateGroupConversation(ctx, "g1", nil); err != nil {
		t.Fatal(err)
	}
	if len(got.query) != 0 {
		t.Errorf("no-op update sent query %v", got.query)
	}
	empty := ""
	c, got = groupServer(t, `{"id":"g1"}`)
	if _, err := c.UpdateGroupConversation(ctx, "g1",
		&UpdateGroupConversationOptions{Description: &empty}); err != nil {
		t.Fatal(err)
	}
	if _, present := got.query["description"]; !present {
		t.Error("clearing the description omitted the parameter entirely")
	}
	if _, present := got.query["title"]; present {
		t.Error("an untouched title was sent")
	}
}

// Routes and verbs, in one table. Each is a place a typo produces a 404 that
// reads like a missing group rather than a wrong path.
func TestGroupRoutes(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		call       func(*Client) error
		wantMethod string
		wantPath   string
	}{
		{"ListGroupMembers", func(c *Client) error {
			_, err := c.ListGroupMembers(ctx, "g1")
			return err
		}, http.MethodGet, "/messages/groups/g1/members"},
		{"AddGroupMember", func(c *Client) error {
			_, err := c.AddGroupMember(ctx, "g1", "bob")
			return err
		}, http.MethodPost, "/messages/groups/g1/members"},
		{"RemoveGroupMember", func(c *Client) error {
			_, err := c.RemoveGroupMember(ctx, "g1", "u2")
			return err
		}, http.MethodDelete, "/messages/groups/g1/members/u2"},
		{"SetGroupAdmin", func(c *Client) error {
			_, err := c.SetGroupAdmin(ctx, "g1", "u2", true)
			return err
		}, http.MethodPut, "/messages/groups/g1/members/u2/admin"},
		{"TransferGroupCreator", func(c *Client) error {
			_, err := c.TransferGroupCreator(ctx, "g1", "bob")
			return err
		}, http.MethodPost, "/messages/groups/g1/transfer-creator"},
		{"RespondToGroupInvite", func(c *Client) error {
			_, err := c.RespondToGroupInvite(ctx, "g1", true)
			return err
		}, http.MethodPost, "/messages/groups/g1/invite/respond"},
		{"PinGroupMessage", func(c *Client) error {
			_, err := c.PinGroupMessage(ctx, "g1", "m1")
			return err
		}, http.MethodPost, "/messages/groups/g1/messages/m1/pin"},
		{"UnpinGroupMessage", func(c *Client) error {
			_, err := c.UnpinGroupMessage(ctx, "g1", "m1")
			return err
		}, http.MethodDelete, "/messages/groups/g1/messages/m1/pin"},
		{"MarkGroupAllRead", func(c *Client) error {
			_, err := c.MarkGroupAllRead(ctx, "g1")
			return err
		}, http.MethodPost, "/messages/groups/g1/read-all"},
		{"UnmuteGroupConversation", func(c *Client) error {
			_, err := c.UnmuteGroupConversation(ctx, "g1")
			return err
		}, http.MethodPost, "/messages/groups/g1/unmute"},
		{"SnoozeGroupConversation", func(c *Client) error {
			_, err := c.SnoozeGroupConversation(ctx, "g1", "8h")
			return err
		}, http.MethodPost, "/messages/groups/g1/snooze"},
		{"UnsnoozeGroupConversation", func(c *Client) error {
			_, err := c.UnsnoozeGroupConversation(ctx, "g1")
			return err
		}, http.MethodPost, "/messages/groups/g1/unsnooze"},
		{"SearchGroupMessages", func(c *Client) error {
			_, err := c.SearchGroupMessages(ctx, "g1", "term", nil)
			return err
		}, http.MethodGet, "/messages/groups/g1/search"},
		{"UpdateGroupConversation", func(c *Client) error {
			_, err := c.UpdateGroupConversation(ctx, "g1", nil)
			return err
		}, http.MethodPatch, "/messages/groups/g1"},
		{"GetGroupConversation", func(c *Client) error {
			_, err := c.GetGroupConversation(ctx, "g1", nil)
			return err
		}, http.MethodGet, "/messages/groups/g1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, got := groupServer(t, `{"id":"g1","invite_status":"accepted","snoozed_until":"x"}`)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if got.method != tc.wantMethod {
				t.Errorf("method = %s, want %s", got.method, tc.wantMethod)
			}
			if got.path != tc.wantPath {
				t.Errorf("path = %s, want %s", got.path, tc.wantPath)
			}
		})
	}
}

func TestSearchGroupMessagesResults(t *testing.T) {
	// The body GroupSearchOut actually produces: q / count / results /
	// pagination, with each hit a FLAT MessageOut carrying body_highlight.
	// An earlier version of this file modelled {hits, total, has_more} with a
	// nested {message, highlight} — every field wrong, so the method returned
	// an empty struct on success and the tests passed because the fixture was
	// wrong in the same direction.
	c, got := groupServer(t, `{"q":"b","count":1,
	  "results":[{"id":"m1","body":"a b c","body_highlight":"a <mark>b</mark> c"}],
	  "pagination":{"has_more":false,"total":1}}`)
	res, err := c.SearchGroupMessages(context.Background(), "g1", "b",
		&SearchGroupMessagesOptions{Limit: 5, Offset: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got.query.Get("q") != "b" || got.query.Get("limit") != "5" || got.query.Get("offset") != "10" {
		t.Errorf("query = %v", got.query)
	}
	if res.Query != "b" || res.Count != 1 {
		t.Errorf("envelope: q=%q count=%d", res.Query, res.Count)
	}
	if len(res.Results) != 1 {
		t.Fatalf("results = %+v", res.Results)
	}
	hit := res.Results[0]
	// The hit embeds Message, so the message fields are flat on the wire.
	if hit.ID != "m1" || hit.Body != "a b c" {
		t.Errorf("hit message not decoded: %+v", hit.Message)
	}
	if !strings.Contains(hit.BodyHighlight, "<mark>") {
		t.Errorf("body_highlight = %q", hit.BodyHighlight)
	}
	if res.Pagination.HasMore == nil || *res.Pagination.HasMore {
		t.Errorf("pagination = %+v", res.Pagination)
	}
	if res.MoreAfter(5) {
		t.Error("MoreAfter true for has_more:false")
	}
	// Same tri-state as PaginatedList: absent is not false.
	res.Pagination.HasMore = nil
	res.Results = make([]GroupSearchHit, 5)
	if !res.MoreAfter(5) {
		t.Error("with has_more absent and a full page, MoreAfter should fall back")
	}
}

// The regression guard for the shape that shipped: the real body must not
// decode into the imagined one, and the imagined body must not decode into the
// real struct. Without both, "it decodes" is true of a struct that accepts
// anything.
func TestSearchResultsRejectTheImaginedShape(t *testing.T) {
	real := []byte(`{"q":"b","count":1,
	  "results":[{"id":"m1","body_highlight":"x"}],"pagination":{"has_more":false}}`)
	var res GroupSearchResults
	if err := json.Unmarshal(real, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 1 || res.Count != 1 {
		t.Fatalf("the real body did not populate the struct: %+v", res)
	}

	imagined := []byte(`{"hits":[{"message":{"id":"m1"},"highlight":"x"}],
	  "total":1,"has_more":false}`)
	var bad GroupSearchResults
	if err := json.Unmarshal(imagined, &bad); err != nil {
		t.Fatal(err)
	}
	if len(bad.Results) != 0 || bad.Count != 0 {
		t.Error("the imagined shape populated the struct — the tags are not what the server sends")
	}
	// And it is REACHABLE rather than lost, which is what Extra is for.
	if _, ok := bad.Extra["hits"]; !ok {
		t.Errorf("unmodelled keys did not land in Extra: %v", bad.Extra)
	}
}

func TestGroupAvatarRoundTrip(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nxxxx")
	// GroupAvatarUploadOut is {avatar_url}, not the profile-avatar shape.
	c, got := groupServer(t, `{"avatar_url":"/messages/groups/g1/avatar"}`)
	up, err := c.UploadGroupAvatar(context.Background(), "g1", "a.png", "image/png", png)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/messages/groups/g1/avatar" || got.method != http.MethodPost {
		t.Errorf("%s %s", got.method, got.path)
	}
	if up.AvatarURL != "/messages/groups/g1/avatar" {
		t.Errorf("upload = %+v", up)
	}

	// The fetch returns image bytes, not JSON.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write(png)
	}))
	defer srv.Close()
	b, err := NewClient("col_x", WithBaseURL(srv.URL)).GetGroupAvatar(context.Background(), "g1")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(png) {
		t.Errorf("got %d bytes, want %d", len(b), len(png))
	}
}

func TestGroupErrorsPropagate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":{"code":"AUTH_FORBIDDEN","message":"not a member"}}`))
	}))
	defer srv.Close()
	c := NewClient("col_x", WithBaseURL(srv.URL), WithRetry(RetryConfig{}))

	if _, err := c.GetGroupConversation(context.Background(), "g1", nil); err == nil {
		t.Error("GetGroupConversation swallowed a 403")
	}
	if _, err := c.MarkGroupAllRead(context.Background(), "g1"); err == nil {
		t.Error("MarkGroupAllRead swallowed a 403")
	}
}

func TestGetGroupConversationPaging(t *testing.T) {
	ctx := context.Background()

	// Zero means "server default" and must not be sent — limit=0 is a
	// request for no messages, which is a different thing.
	c, got := groupServer(t, `{"id":"g1"}`)
	if _, err := c.GetGroupConversation(ctx, "g1", &GetGroupConversationOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(got.query) != 0 {
		t.Errorf("zero options sent query %v, want none", got.query)
	}

	c, got = groupServer(t, `{"id":"g1"}`)
	if _, err := c.GetGroupConversation(ctx, "g1",
		&GetGroupConversationOptions{Limit: 50, Offset: 100}); err != nil {
		t.Fatal(err)
	}
	if got.query.Get("limit") != "50" || got.query.Get("offset") != "100" {
		t.Errorf("query = %v", got.query)
	}
}

func TestGroupIDsAreNotDoubleEscaped(t *testing.T) {
	// Group and message ids go into the path verbatim, matching every other
	// id-bearing route in this package. A conv id with a slash would be a
	// different route, not an escaped segment — pinned so a later "fix" that
	// adds PathEscape has to justify itself against the rest of the package.
	c, got := groupServer(t, `{"id":"g1"}`)
	if _, err := c.PinGroupMessage(context.Background(),
		"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"); err != nil {
		t.Fatal(err)
	}
	want := "/messages/groups/11111111-1111-4111-8111-111111111111/messages/" +
		"22222222-2222-4222-8222-222222222222/pin"
	if got.path != want {
		t.Errorf("path = %s\nwant %s", got.path, want)
	}
}

// The promoted-method trap, pinned. A type that embeds one with its own
// UnmarshalJSON gets that method PROMOTED, so the default decode runs the
// embedded type's unmarshaller over the whole object and every field declared
// on the outer struct is silently skipped.
//
// This is not hypothetical here: it is why GroupSearchHit.BodyHighlight came
// back empty from a body that carried it, and it would recur for any future
// type embedding Message. Both halves must decode.
func TestEmbeddedMessageDoesNotSwallowOuterFields(t *testing.T) {
	var hit GroupSearchHit
	if err := json.Unmarshal([]byte(
		`{"id":"m1","body":"a b c","body_highlight":"a <mark>b</mark> c"}`), &hit); err != nil {
		t.Fatal(err)
	}
	if hit.ID != "m1" || hit.Body != "a b c" {
		t.Errorf("embedded Message half lost: %+v", hit.Message)
	}
	if hit.BodyHighlight == "" {
		t.Error("outer field swallowed by the promoted Message.UnmarshalJSON")
	}

	var msg GroupMessage
	if err := json.Unmarshal([]byte(`{"id":"m2","body":"hi","read_count":4}`), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.ID != "m2" {
		t.Errorf("embedded half lost: %+v", msg.Message)
	}
	if msg.ReadCount != 4 {
		t.Errorf("ReadCount = %d, want 4 — outer field swallowed", msg.ReadCount)
	}
}
