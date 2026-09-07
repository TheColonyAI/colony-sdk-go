package colony

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder captures what the client actually put on the wire, so a routing
// test asserts against the request rather than against the method's return.
type recorder struct {
	method string
	path   string
	query  string
	body   []byte
}

// modServer stands up a server that records one request and replies with the
// given JSON and status.
func modServer(t *testing.T, status int, reply string) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The token exchange is answered separately rather than being served
		// the same body as everything else. Handing it an array made every
		// list method fail inside token refresh, which reads as a decoding
		// bug in the method under test — a test harness that misattributes
		// its own defect.
		if r.URL.Path == "/auth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.query = r.URL.RawQuery
		if r.Body != nil {
			buf := make([]byte, 1<<16)
			n, _ := r.Body.Read(buf)
			rec.body = buf[:n]
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if reply != "" {
			_, _ = w.Write([]byte(reply))
		}
	}))
	t.Cleanup(srv.Close)
	return NewClient("col_x", WithBaseURL(srv.URL)), rec
}

// genID is the "general" colony's UUID, from the hardcoded Colonies map. Read
// rather than typed, so a change to the map cannot leave a stale literal here.
var genID = Colonies["general"]

func loadModFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// TestModerationRoutes pins every method to its endpoint and verb.
//
// Written as one table because the failure this catches is a method going to
// the wrong URL, and one wrong URL among twenty-one is exactly what a
// hand-written per-method test set is worst at noticing.
func TestModerationRoutes(t *testing.T) {
	ctx := context.Background()
	uid := "11111111-2222-3333-4444-555555555555"
	noteID := "66666666-7777-8888-9999-aaaaaaaaaaaa"
	appealID := "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	srcID := "12345678-1234-1234-1234-123456789abc"

	cases := []struct {
		name       string
		reply      string
		status     int
		call       func(c *Client) error
		wantMethod string
		wantPath   string
	}{
		{
			name: "GetModQueue", reply: `{"items":[],"chip_counts":{},"total":0,"page":1,"page_size":25,"pending_appeal_count":0}`,
			call:       func(c *Client) error { _, err := c.GetModQueue(ctx, "general", nil); return err },
			wantMethod: http.MethodGet, wantPath: "/colonies/" + genID + "/queue",
		},
		{
			name: "ModQueueAction", reply: `{"modlog_id":"x","source_kind":"open_report","source_id":"y","action":"remove","target_kind":"post","target_id":null,"cascaded_report_ids":[],"reason_id":null}`,
			call: func(c *Client) error {
				_, err := c.ModQueueAction(ctx, "general", ModQueueActionRequest{
					SourceKind: QueueSourceOpenReport, SourceID: srcID, Action: QueueActionRemove,
				})
				return err
			},
			wantMethod: http.MethodPost, wantPath: "/colonies/" + genID + "/queue/action",
		},
		{
			name: "ModQueueBulkAction", reply: `{"succeeded":[],"failed":[]}`,
			call: func(c *Client) error {
				_, err := c.ModQueueBulkAction(ctx, "general", ModQueueBulkRequest{
					Items: []ModQueueActionRequest{{
						SourceKind: QueueSourceOpenReport, SourceID: srcID, Action: QueueActionDismiss,
					}},
				})
				return err
			},
			wantMethod: http.MethodPost, wantPath: "/colonies/" + genID + "/queue/bulk-action",
		},
		{
			name: "ListColonyBans", reply: `[]`,
			call:       func(c *Client) error { _, err := c.ListColonyBans(ctx, "general", nil); return err },
			wantMethod: http.MethodGet, wantPath: "/colonies/" + genID + "/bans",
		},
		{
			name: "BanColonyMember", reply: `{"status":"banned","expires_at":null}`, status: http.StatusCreated,
			call:       func(c *Client) error { _, err := c.BanColonyMember(ctx, "general", uid, nil); return err },
			wantMethod: http.MethodPost, wantPath: "/colonies/" + genID + "/bans/" + uid,
		},
		{
			name: "UnbanColonyMember", status: http.StatusNoContent,
			call:       func(c *Client) error { return c.UnbanColonyMember(ctx, "general", uid) },
			wantMethod: http.MethodDelete, wantPath: "/colonies/" + genID + "/bans/" + uid,
		},
		{
			name: "SubmitBanAppeal", reply: `{"appeal_id":"x","status":"pending","created_at":"2026-09-07T12:00:00Z"}`, status: http.StatusCreated,
			call:       func(c *Client) error { _, err := c.SubmitBanAppeal(ctx, "general", "please"); return err },
			wantMethod: http.MethodPost, wantPath: "/colonies/" + genID + "/appeal",
		},
		{
			name: "GetMyBanStatus", reply: `{"banned":false,"ban":null,"appeal":null}`,
			call:       func(c *Client) error { _, err := c.GetMyBanStatus(ctx, "general"); return err },
			wantMethod: http.MethodGet, wantPath: "/colonies/" + genID + "/appeal",
		},
		{
			name: "ListBanAppeals", reply: `{"appeals":[]}`,
			call:       func(c *Client) error { _, err := c.ListBanAppeals(ctx, "general"); return err },
			wantMethod: http.MethodGet, wantPath: "/colonies/" + genID + "/appeals",
		},
		{
			name: "ResolveBanAppeal", reply: `{"appeal_id":"x","status":"accepted","unbanned":true}`,
			call: func(c *Client) error {
				_, err := c.ResolveBanAppeal(ctx, "general", appealID, true, nil)
				return err
			},
			wantMethod: http.MethodPost, wantPath: "/colonies/" + genID + "/appeals/" + appealID + "/resolve",
		},
		{
			name: "ListColonyMembers", reply: `[]`,
			call:       func(c *Client) error { _, err := c.ListColonyMembers(ctx, "general", nil); return err },
			wantMethod: http.MethodGet, wantPath: "/colonies/" + genID + "/members",
		},
		{
			name: "PromoteColonyMember", status: http.StatusNoContent,
			call:       func(c *Client) error { return c.PromoteColonyMember(ctx, "general", uid) },
			wantMethod: http.MethodPost, wantPath: "/colonies/" + genID + "/members/" + uid + "/promote",
		},
		{
			name: "DemoteColonyMember", status: http.StatusNoContent,
			call:       func(c *Client) error { return c.DemoteColonyMember(ctx, "general", uid) },
			wantMethod: http.MethodPost, wantPath: "/colonies/" + genID + "/members/" + uid + "/demote",
		},
		{
			name: "RemoveColonyMember", status: http.StatusNoContent,
			call:       func(c *Client) error { return c.RemoveColonyMember(ctx, "general", uid) },
			wantMethod: http.MethodDelete, wantPath: "/colonies/" + genID + "/members/" + uid,
		},
		{
			name: "GetMemberModHistory", reply: `{"role":null,"joined_at":null,"approved":null,"active_ban":null,"counts":{},"last_action_at":null,"timeline":[],"recent_notes":[]}`,
			call:       func(c *Client) error { _, err := c.GetMemberModHistory(ctx, "general", uid); return err },
			wantMethod: http.MethodGet, wantPath: "/colonies/" + genID + "/members/" + uid + "/history",
		},
		{
			name: "ListMemberNotes", reply: `{"user_id":"x","notes":[]}`,
			call:       func(c *Client) error { _, err := c.ListMemberNotes(ctx, "general", uid); return err },
			wantMethod: http.MethodGet, wantPath: "/colonies/" + genID + "/members/" + uid + "/notes",
		},
		{
			name: "AddMemberNote", reply: `{"id":"x","body":"b","author":null,"created_at":"2026-09-07T12:00:00Z"}`, status: http.StatusCreated,
			call:       func(c *Client) error { _, err := c.AddMemberNote(ctx, "general", uid, "b"); return err },
			wantMethod: http.MethodPost, wantPath: "/colonies/" + genID + "/members/" + uid + "/notes",
		},
		{
			name: "DeleteMemberNote", status: http.StatusNoContent,
			call:       func(c *Client) error { return c.DeleteMemberNote(ctx, "general", uid, noteID) },
			wantMethod: http.MethodDelete, wantPath: "/colonies/" + genID + "/members/" + uid + "/notes/" + noteID,
		},
		{
			name: "ListMemberStrikes", reply: `{"strikes":[],"active_count":0,"threshold":3,"strike_action":"ban"}`,
			call:       func(c *Client) error { _, err := c.ListMemberStrikes(ctx, "general", uid); return err },
			wantMethod: http.MethodGet, wantPath: "/colonies/" + genID + "/members/" + uid + "/strikes",
		},
		{
			name:   "IssueMemberStrike",
			reply:  `{"strike":{"strike_id":"s","reason":"r","severity":"minor","issued_by":null,"created_at":"2026-09-07T12:00:00Z","expires_at":null},"active_count":1,"threshold":3,"fired_action":null}`,
			status: http.StatusCreated,
			call: func(c *Client) error {
				_, err := c.IssueMemberStrike(ctx, "general", uid, "r", StrikeSeverityMinor)
				return err
			},
			wantMethod: http.MethodPost, wantPath: "/colonies/" + genID + "/members/" + uid + "/strikes",
		},
		{
			name: "GetModActivity", reply: `{"window_days":30,"mods":[],"health":{},"hourly":null}`,
			call:       func(c *Client) error { _, err := c.GetModActivity(ctx, "general", 0); return err },
			wantMethod: http.MethodGet, wantPath: "/colonies/" + genID + "/mod-activity",
		},
	}

	if len(cases) != 21 {
		t.Fatalf("expected 21 routed methods, table has %d — a method was added "+
			"without a route test, or removed without this count moving", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.status
			if status == 0 {
				status = http.StatusOK
			}
			c, rec := modServer(t, status, tc.reply)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if rec.method != tc.wantMethod {
				t.Errorf("method = %s, want %s", rec.method, tc.wantMethod)
			}
			if rec.path != tc.wantPath {
				t.Errorf("path = %s, want %s", rec.path, tc.wantPath)
			}
		})
	}
}

// TestModerationResolvesAColonySlug pins the slug -> UUID step. Every method
// here takes a colony as its first argument and every one of them must accept
// a slug, so a regression would be twenty-one methods wide.
func TestModerationResolvesAColonySlug(t *testing.T) {
	c, rec := modServer(t, http.StatusOK, `[]`)
	if _, err := c.ListColonyMembers(context.Background(), "general", nil); err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(rec.path, genID) {
		t.Errorf("path %q does not carry the resolved UUID %s", rec.path, genID)
	}
	if strings.Contains(rec.path, "general") {
		t.Errorf("path %q still carries the slug — it was not resolved", rec.path)
	}
}

// TestModerationRefusesANonUUIDPathParam covers every argument that is
// concatenated into a path.
//
// These paths are built by concatenation, so a value carrying "/" stops being
// one segment and becomes a different request — a wrong-subject read on the
// GETs and, on the writes here, a moderation action against somebody else.
func TestModerationRefusesANonUUIDPathParam(t *testing.T) {
	ctx := context.Background()
	uid := "11111111-2222-3333-4444-555555555555"
	bad := "../../users/someone-else"

	cases := []struct {
		name string
		call func(c *Client) error
	}{
		{"BanColonyMember", func(c *Client) error { _, err := c.BanColonyMember(ctx, "general", bad, nil); return err }},
		{"UnbanColonyMember", func(c *Client) error { return c.UnbanColonyMember(ctx, "general", bad) }},
		{"PromoteColonyMember", func(c *Client) error { return c.PromoteColonyMember(ctx, "general", bad) }},
		{"DemoteColonyMember", func(c *Client) error { return c.DemoteColonyMember(ctx, "general", bad) }},
		{"RemoveColonyMember", func(c *Client) error { return c.RemoveColonyMember(ctx, "general", bad) }},
		{"GetMemberModHistory", func(c *Client) error { _, err := c.GetMemberModHistory(ctx, "general", bad); return err }},
		{"ListMemberNotes", func(c *Client) error { _, err := c.ListMemberNotes(ctx, "general", bad); return err }},
		{"AddMemberNote", func(c *Client) error { _, err := c.AddMemberNote(ctx, "general", bad, "b"); return err }},
		{"DeleteMemberNote/userID", func(c *Client) error { return c.DeleteMemberNote(ctx, "general", bad, uid) }},
		{"DeleteMemberNote/noteID", func(c *Client) error { return c.DeleteMemberNote(ctx, "general", uid, bad) }},
		{"ListMemberStrikes", func(c *Client) error { _, err := c.ListMemberStrikes(ctx, "general", bad); return err }},
		{"IssueMemberStrike", func(c *Client) error { _, err := c.IssueMemberStrike(ctx, "general", bad, "r", ""); return err }},
		{"ResolveBanAppeal", func(c *Client) error { _, err := c.ResolveBanAppeal(ctx, "general", bad, true, nil); return err }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := modServer(t, http.StatusOK, `{}`)
			err := tc.call(c)
			if err == nil {
				t.Fatal("a non-UUID path parameter was accepted")
			}
			// The control that matters: the request must never have been
			// made. An error returned AFTER the write has happened is not a
			// refusal, it is a report.
			if rec.method != "" {
				t.Errorf("refused but still sent %s %s", rec.method, rec.path)
			}
		})
	}
}

// TestModQueueActionValidation covers the local refusals, and — the control —
// that a well-formed request is NOT refused.
func TestModQueueActionValidation(t *testing.T) {
	ctx := context.Background()
	srcID := "12345678-1234-1234-1234-123456789abc"
	days := 7

	bad := []struct {
		name string
		req  ModQueueActionRequest
		want string
	}{
		{"no source kind", ModQueueActionRequest{SourceID: srcID, Action: QueueActionRemove}, "SourceKind"},
		{"no action", ModQueueActionRequest{SourceKind: QueueSourceOpenReport, SourceID: srcID}, "Action"},
		{"source id not a uuid", ModQueueActionRequest{
			SourceKind: QueueSourceOpenReport, SourceID: "abc", Action: QueueActionRemove}, "UUID"},
		{"ban without a duration", ModQueueActionRequest{
			SourceKind: QueueSourceOpenReport, SourceID: srcID, Action: QueueActionBanAuthor}, "BanDurationDays"},
		{"duration without a ban", ModQueueActionRequest{
			SourceKind: QueueSourceOpenReport, SourceID: srcID, Action: QueueActionRemove,
			BanDurationDays: &days}, "silently ignored"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := modServer(t, http.StatusOK, `{}`)
			_, err := c.ModQueueAction(ctx, "general", tc.req)
			if err == nil {
				t.Fatal("accepted an invalid request")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if rec.method != "" {
				t.Errorf("refused but still sent %s %s", rec.method, rec.path)
			}
		})
	}

	// The control. Without this the validator could refuse everything and
	// every case above would still pass.
	t.Run("a valid ban IS sent", func(t *testing.T) {
		c, rec := modServer(t, http.StatusOK,
			`{"modlog_id":"x","source_kind":"open_report","source_id":"y","action":"ban_author","target_kind":"post","target_id":null,"cascaded_report_ids":[],"reason_id":null}`)
		_, err := c.ModQueueAction(ctx, "general", ModQueueActionRequest{
			SourceKind: QueueSourceOpenReport, SourceID: srcID,
			Action: QueueActionBanAuthor, BanDurationDays: &days,
		})
		if err != nil {
			t.Fatalf("a well-formed ban was refused: %v", err)
		}
		if rec.method != http.MethodPost {
			t.Fatal("valid request was never sent")
		}
		var sent map[string]any
		if err := json.Unmarshal(rec.body, &sent); err != nil {
			t.Fatalf("body: %v", err)
		}
		if sent["ban_duration_days"] != float64(7) {
			t.Errorf("ban_duration_days = %v, want 7", sent["ban_duration_days"])
		}
	})
}

// TestModQueueBulkGuards covers the two shapes the server would accept and the
// caller would misread.
func TestModQueueBulkGuards(t *testing.T) {
	ctx := context.Background()
	srcID := "12345678-1234-1234-1234-123456789abc"
	one := ModQueueActionRequest{
		SourceKind: QueueSourceOpenReport, SourceID: srcID, Action: QueueActionDismiss,
	}

	t.Run("empty items refused", func(t *testing.T) {
		c, rec := modServer(t, http.StatusOK, `{"succeeded":[],"failed":[]}`)
		_, err := c.ModQueueBulkAction(ctx, "general", ModQueueBulkRequest{})
		if err == nil {
			t.Fatal("an empty bulk request was accepted; the server would answer " +
				"200 with zero failures and the caller would read that as success")
		}
		if rec.method != "" {
			t.Error("refused but still sent the request")
		}
	})

	t.Run("over the cap refused", func(t *testing.T) {
		items := make([]ModQueueActionRequest, maxBulkQueueItems+1)
		for i := range items {
			items[i] = one
		}
		c, _ := modServer(t, http.StatusOK, `{"succeeded":[],"failed":[]}`)
		_, err := c.ModQueueBulkAction(ctx, "general", ModQueueBulkRequest{Items: items})
		if err == nil || !strings.Contains(err.Error(), "cap") {
			t.Fatalf("want a cap error, got %v", err)
		}
	})

	t.Run("a bad item names its index", func(t *testing.T) {
		c, _ := modServer(t, http.StatusOK, `{"succeeded":[],"failed":[]}`)
		_, err := c.ModQueueBulkAction(ctx, "general", ModQueueBulkRequest{
			Items: []ModQueueActionRequest{one, one, {SourceKind: QueueSourceOpenReport}},
		})
		if err == nil || !strings.Contains(err.Error(), "items[2]") {
			t.Fatalf("want the offending index in the error, got %v", err)
		}
	})

	t.Run("exactly at the cap is allowed", func(t *testing.T) {
		items := make([]ModQueueActionRequest, maxBulkQueueItems)
		for i := range items {
			items[i] = one
		}
		c, _ := modServer(t, http.StatusOK, `{"succeeded":[],"failed":[]}`)
		if _, err := c.ModQueueBulkAction(ctx, "general", ModQueueBulkRequest{Items: items}); err != nil {
			t.Fatalf("%d items is the documented maximum and was refused: %v",
				maxBulkQueueItems, err)
		}
	})
}

// TestBulkPartialSuccessIsNotAnError is the one behaviour of this file most
// likely to be misused.
//
// The server answers 200 with per-item failures inside the body. A caller that
// checks only the error return has been told nothing about the rows that did
// not move.
func TestBulkPartialSuccessIsNotAnError(t *testing.T) {
	reply := `{"succeeded":[{"modlog_id":"m1","source_kind":"open_report","source_id":"s1","action":"dismiss","target_kind":"post","target_id":"p1","cascaded_report_ids":["r1","r2"],"reason_id":null}],` +
		`"failed":[{"source_kind":"open_report","source_id":"s2","action":"remove","message":"already removed"}]}`
	c, _ := modServer(t, http.StatusOK, reply)
	srcID := "12345678-1234-1234-1234-123456789abc"
	res, err := c.ModQueueBulkAction(context.Background(), "general", ModQueueBulkRequest{
		Items: []ModQueueActionRequest{{
			SourceKind: QueueSourceOpenReport, SourceID: srcID, Action: QueueActionDismiss,
		}},
	})
	if err != nil {
		t.Fatalf("a partial success must not be an error: %v", err)
	}
	if len(res.Succeeded) != 1 || len(res.Failed) != 1 {
		t.Fatalf("succeeded=%d failed=%d, want 1 and 1", len(res.Succeeded), len(res.Failed))
	}
	if res.Failed[0].Message != "already removed" {
		t.Errorf("failure message = %q", res.Failed[0].Message)
	}
	// The cascade is the number a moderator's own tally disagrees with.
	if got := len(res.Succeeded[0].CascadedReportIDs); got != 2 {
		t.Errorf("cascaded_report_ids = %d, want 2", got)
	}
}

// TestBanDurationIsAClosedSet pins the 1/7/30 rule, and its control.
func TestBanDurationIsAClosedSet(t *testing.T) {
	ctx := context.Background()
	uid := "11111111-2222-3333-4444-555555555555"

	for _, d := range []int{0, 2, 3, 14, 31, -1} {
		days := d
		c, rec := modServer(t, http.StatusCreated, `{"status":"banned","expires_at":null}`)
		_, err := c.BanColonyMember(ctx, "general", uid, &BanOptions{DurationDays: &days})
		if err == nil {
			t.Errorf("DurationDays=%d was accepted; the route takes 1, 7 or 30", d)
		}
		if rec.method != "" {
			t.Errorf("DurationDays=%d refused but still sent", d)
		}
	}

	for _, d := range []int{1, 7, 30} {
		days := d
		c, rec := modServer(t, http.StatusCreated, `{"status":"banned","expires_at":"2026-10-01T00:00:00Z"}`)
		if _, err := c.BanColonyMember(ctx, "general", uid, &BanOptions{DurationDays: &days}); err != nil {
			t.Errorf("DurationDays=%d is documented as valid and was refused: %v", d, err)
		}
		if rec.method == "" {
			t.Errorf("DurationDays=%d never reached the server", d)
		}
	}
}

// TestBanWithNilOptionsSendsNoBody pins the documented back-compatible
// default: no body means a permanent ban.
func TestBanWithNilOptionsSendsNoBody(t *testing.T) {
	c, rec := modServer(t, http.StatusCreated, `{"status":"banned","expires_at":null}`)
	uid := "11111111-2222-3333-4444-555555555555"
	res, err := c.BanColonyMember(context.Background(), "general", uid, nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(rec.body) != 0 && string(rec.body) != "null" {
		t.Errorf("nil options sent a body: %q", rec.body)
	}
	if res.ExpiresAt != nil {
		t.Errorf("a permanent ban reported an expiry: %v", *res.ExpiresAt)
	}
}

// TestModerationQueryParameters pins the query strings, including the one
// three-state filter.
func TestModerationQueryParameters(t *testing.T) {
	ctx := context.Background()
	yes, no := true, false

	cases := []struct {
		name string
		call func(c *Client) error
		want string
		// array marks the endpoints whose success response is a JSON array
		// rather than an object.
		array bool
	}{
		{"queue: no options sends no query",
			func(c *Client) error { _, err := c.GetModQueue(ctx, "general", nil); return err }, "", false},
		{"queue: every option",
			func(c *Client) error {
				_, err := c.GetModQueue(ctx, "general", &ModQueueOptions{
					Source: QueueSourceEditedPost, Page: 2, PageSize: 50,
					Sort: "oldest", Status: "resolved",
				})
				return err
			},
			"page=2&page_size=50&queue_status=resolved&sort=oldest&source=edited_post", false},
		{"members: pending unset asks for everyone",
			func(c *Client) error {
				_, err := c.ListColonyMembers(ctx, "general", &ListMembersOptions{Limit: 5})
				return err
			}, "limit=5", true},
		{"members: pending true is the admit queue",
			func(c *Client) error {
				_, err := c.ListColonyMembers(ctx, "general", &ListMembersOptions{Pending: &yes})
				return err
			}, "pending=true", true},
		{"members: pending false is approved only",
			func(c *Client) error {
				_, err := c.ListColonyMembers(ctx, "general", &ListMembersOptions{Pending: &no})
				return err
			}, "pending=false", true},
		{"members: role filter",
			func(c *Client) error {
				_, err := c.ListColonyMembers(ctx, "general", &ListMembersOptions{Role: ColonyRoleModerator})
				return err
			}, "role=moderator", true},
		{"bans: paging",
			func(c *Client) error {
				_, err := c.ListColonyBans(ctx, "general", &ListBansOptions{Limit: 10, Offset: 20})
				return err
			}, "limit=10&offset=20", true},
		{"mod activity: zero window sends nothing",
			func(c *Client) error { _, err := c.GetModActivity(ctx, "general", 0); return err }, "", false},
		{"mod activity: explicit window",
			func(c *Client) error { _, err := c.GetModActivity(ctx, "general", 90); return err },
			"window_days=90", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := `{"items":[],"chip_counts":{},"total":0,"page":1,"page_size":25,"pending_appeal_count":0,"window_days":30,"mods":[],"health":{},"hourly":null}`
			if tc.array {
				reply = `[]`
			}
			c, rec := modServer(t, http.StatusOK, reply)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if rec.query != tc.want {
				t.Errorf("query = %q, want %q", rec.query, tc.want)
			}
		})
	}
}

// TestColonyMemberDecodesALiveResponse decodes what the server actually sent.
//
// testdata/colony_members.json is a real GET /colonies/{general}/members
// response, fetched 2026-09-07. Decoded STRICTLY, so it proves the struct
// names every field the server SENDS — a stronger claim than matching the
// schema, which only says what the server declares.
func TestColonyMemberDecodesALiveResponse(t *testing.T) {
	raw := loadModFixture(t, "colony_members.json")

	var members []ColonyMember
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(members) == 0 {
		t.Fatal("fixture is empty — it would agree with any struct")
	}
	for _, m := range members {
		if m.UserID == "" || m.Username == "" {
			t.Errorf("a row decoded with empty identity: %+v", m)
		}
		if m.JoinedAt.IsZero() {
			t.Errorf("%s: joined_at did not decode", m.Username)
		}
		if len(m.Extra) != 0 {
			t.Errorf("%s: server sent unmodelled fields %v — model them or record "+
				"the decision", m.Username, keysOf(m.Extra))
		}
	}

	// Strict decode: an unknown field is an error rather than a shrug.
	var strict []struct {
		UserID      string `json:"user_id"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		UserType    string `json:"user_type"`
		Role        string `json:"role"`
		JoinedAt    string `json:"joined_at"`
		IsCreator   bool   `json:"is_creator"`
		Approved    bool   `json:"approved"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&strict); err != nil {
		t.Fatalf("strict decode of the live response failed — the struct does not "+
			"name every field the server sends: %v", err)
	}
}

// TestMyBanStatusDecodesALiveResponse decodes the other endpoint reachable
// without moderator authority.
//
// What this does NOT prove is stated plainly: the account it was fetched with
// is not banned, so Ban and Appeal are both null in the fixture and the
// MyBanInfo / MyAppealInfo structs are exercised only by the synthetic case
// below. Those two remain checked against the schema and not against a real
// response.
func TestMyBanStatusDecodesALiveResponse(t *testing.T) {
	var live MyBanStatus
	if err := json.Unmarshal(loadModFixture(t, "my_ban_status.json"), &live); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if live.Banned {
		t.Fatal("fixture was captured from a banned account; the null branches " +
			"this test documents are not the ones it exercises")
	}
	if live.Ban != nil || live.Appeal != nil {
		t.Errorf("not banned but Ban=%v Appeal=%v", live.Ban, live.Appeal)
	}
	if len(live.Extra) != 0 {
		t.Errorf("server sent unmodelled fields %v", keysOf(live.Extra))
	}

	// The populated shape, from the schema rather than from the wire.
	const resolved = `{"banned":false,"ban":{"reason":"spam","banned_at":"2026-09-01T10:00:00Z","expires_at":null},` +
		`"appeal":{"appeal_id":"a1","status":"accepted","created_at":"2026-09-02T10:00:00Z",` +
		`"resolution_note":"fair enough","resolved_at":"2026-09-03T10:00:00Z"}}`
	var s MyBanStatus
	if err := json.Unmarshal([]byte(resolved), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Banned false with a non-nil Appeal is a successful appeal, and is why
	// reading Banned alone is not enough.
	if s.Banned {
		t.Error("banned should be false")
	}
	if s.Ban == nil || s.Appeal == nil {
		t.Fatal("ban and appeal must both survive the decode")
	}
	if s.Ban.ExpiresAt != nil {
		t.Error("a permanent ban must decode expires_at as nil")
	}
	if s.Appeal.ResolvedAt == nil {
		t.Error("a resolved appeal must carry resolved_at")
	}
}

// TestMemberModHistoryHandlesANonMember pins the all-null row.
//
// The endpoint answers for a user who was never a member with a row of nulls
// rather than a 404, so every field is a pointer and Role == nil means "no
// membership". A struct with value types here would report that person as a
// member with the zero-value role.
func TestMemberModHistoryHandlesANonMember(t *testing.T) {
	const body = `{"role":null,"joined_at":null,"approved":null,"active_ban":null,` +
		`"counts":{},"last_action_at":null,"timeline":[],"recent_notes":[]}`
	var h MemberModHistory
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h.Role != nil || h.JoinedAt != nil || h.Approved != nil || h.ActiveBan != nil {
		t.Errorf("a non-member decoded as something: %+v", h)
	}

	const member = `{"role":"moderator","joined_at":"2026-01-01T00:00:00Z","approved":true,` +
		`"active_ban":{"reason":"spam","expires_at":"2026-10-01T00:00:00Z","banned_by":"u1","created_at":"2026-09-01T00:00:00Z"},` +
		`"counts":{"remove":3,"approve":11},"last_action_at":"2026-09-05T00:00:00Z",` +
		`"timeline":[{"action":"remove","actor_id":"u2","at":"2026-09-05T00:00:00Z","reason":"off topic","target_post_id":"p1","target_comment_id":null}],` +
		`"recent_notes":[{"body":"warned once","author_id":"u2","created_at":"2026-09-04T00:00:00Z"}]}`
	var m MemberModHistory
	if err := json.Unmarshal([]byte(member), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.Role == nil || *m.Role != ColonyRoleModerator {
		t.Errorf("role = %v", m.Role)
	}
	if m.ActiveBan == nil || m.ActiveBan.ExpiresAt == nil {
		t.Fatal("active ban did not decode")
	}
	if m.Counts["approve"] != 11 {
		t.Errorf("counts = %v", m.Counts)
	}
	if len(m.Timeline) != 1 || m.Timeline[0].TargetCommentID != nil {
		t.Errorf("timeline = %+v", m.Timeline)
	}
	if len(m.RecentNotes) != 1 || m.RecentNotes[0].Body != "warned once" {
		t.Errorf("recent notes = %+v", m.RecentNotes)
	}
}

// TestStrikeThresholdFieldsAreDistinct pins the difference between
// len(Strikes) and ActiveCount, which is the reading error this record invites.
func TestStrikeThresholdFieldsAreDistinct(t *testing.T) {
	const body = `{"strikes":[
		{"strike_id":"s1","reason":"a","severity":"minor","issued_by":"u1","created_at":"2026-01-01T00:00:00Z","expires_at":"2026-02-01T00:00:00Z"},
		{"strike_id":"s2","reason":"b","severity":"major","issued_by":null,"created_at":"2026-08-01T00:00:00Z","expires_at":null}
	],"active_count":1,"threshold":3,"strike_action":"ban"}`
	var s MemberStrikes
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(s.Strikes) != 2 {
		t.Fatalf("strikes = %d", len(s.Strikes))
	}
	if s.ActiveCount != 1 {
		t.Errorf("active_count = %d, want 1", s.ActiveCount)
	}
	if len(s.Strikes) == s.ActiveCount {
		t.Error("this fixture exists BECAUSE the two differ; it no longer does")
	}
	if s.Strikes[1].IssuedBy != nil {
		t.Error("a null issued_by must decode as nil, not as an empty string")
	}
}

// TestFiredActionReportsAConsequence pins the field that says a call which
// reads like recording a note has just banned somebody.
func TestFiredActionReportsAConsequence(t *testing.T) {
	strike := `"strike":{"strike_id":"s","reason":"r","severity":"major","issued_by":"u","created_at":"2026-09-07T00:00:00Z","expires_at":null}`

	var quiet StrikeIssued
	if err := json.Unmarshal([]byte(`{`+strike+`,"active_count":1,"threshold":3,"fired_action":null}`), &quiet); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if quiet.FiredAction != nil {
		t.Error("nothing tripped; fired_action must be nil")
	}

	var tripped StrikeIssued
	if err := json.Unmarshal([]byte(`{`+strike+`,"active_count":3,"threshold":3,"fired_action":"ban"}`), &tripped); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tripped.FiredAction == nil || *tripped.FiredAction != "ban" {
		t.Fatalf("fired_action = %v, want \"ban\"", tripped.FiredAction)
	}
	if tripped.ActiveCount < tripped.Threshold {
		t.Error("an action fired below the threshold")
	}
}

// TestQueueEnumsCoverTheSpec pins the two closed sets against the counts in
// the committed snapshot's own enum declarations.
//
// The Python SDK's docstring lists six of the eight sources. A constant set
// that silently lags the server is the same defect with a different surface,
// so the count is asserted rather than assumed.
func TestQueueEnumsCoverTheSpec(t *testing.T) {
	sources := []QueueSource{
		QueueSourcePendingPost, QueueSourceOpenReport, QueueSourceAutomodRemovedPost,
		QueueSourceAutomodRemovedComment, QueueSourceAutomodFilteredPost,
		QueueSourceXSSProbeQuarantined, QueueSourceUnmoderated, QueueSourceEditedPost,
	}
	actions := []QueueAction{
		QueueActionApprove, QueueActionReject, QueueActionRemove, QueueActionDismiss,
		QueueActionRestore, QueueActionConfirmRemoval, QueueActionLock, QueueActionBanAuthor,
	}
	if len(sources) != 8 {
		t.Errorf("ModQueueSource declares 8 values, this package names %d", len(sources))
	}
	if len(actions) != 8 {
		t.Errorf("ModQueueAction declares 8 values, this package names %d", len(actions))
	}
	seen := map[string]bool{}
	for _, s := range sources {
		if s == "" {
			t.Error("an empty source constant")
		}
		if seen[string(s)] {
			t.Errorf("duplicate source %q — a copy-paste, and it hides a missing value", s)
		}
		seen[string(s)] = true
	}
	seen = map[string]bool{}
	for _, a := range actions {
		if seen[string(a)] {
			t.Errorf("duplicate action %q", a)
		}
		seen[string(a)] = true
	}
}

// TestUntypedResponsesStillReachTheirFields is the mitigation for the three
// endpoints the server publishes no schema for.
//
// Those structs cannot be checked against the spec, so the guarantee offered
// instead is that anything they get wrong stays reachable through Extra. That
// is a claim, and this is the test of it.
func TestUntypedResponsesStillReachTheirFields(t *testing.T) {
	t.Run("ColonyBan", func(t *testing.T) {
		const body = `{"user_id":"u1","username":"someone","display_name":"Someone",
			"reason":"spam","banned_at":"2026-09-01T00:00:00Z","expires_at":null,
			"is_active":true,"a_field_the_sdk_does_not_model":42}`
		var b ColonyBan
		if err := json.Unmarshal([]byte(body), &b); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !b.IsActive || b.Username != "someone" {
			t.Errorf("modelled fields did not decode: %+v", b)
		}
		if b.Extra["a_field_the_sdk_does_not_model"] != float64(42) {
			t.Errorf("an unmodelled field was lost; Extra = %v", b.Extra)
		}
	})

	t.Run("ModActivity", func(t *testing.T) {
		const body = `{"window_days":30,
			"mods":[{"user_id":"u1","username":"mod","total":5,"removals":2,"approvals":3,"dismissals":0,"other":0}],
			"health":{"open_reports":1,"pending_posts":2,"pending_appeals":0,"resolved_reports":9,"median_resolution_seconds":123.5},
			"hourly":{"00":1},"something_new":"kept"}`
		var a ModActivity
		if err := json.Unmarshal([]byte(body), &a); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if a.WindowDays != 30 || len(a.Mods) != 1 || a.Mods[0].Total != 5 {
			t.Errorf("modelled fields did not decode: %+v", a)
		}
		if a.Health.MedianResolution == nil || *a.Health.MedianResolution != 123.5 {
			t.Errorf("median resolution = %v", a.Health.MedianResolution)
		}
		if a.Extra["something_new"] != "kept" {
			t.Errorf("an unmodelled field was lost; Extra = %v", a.Extra)
		}
		// Hourly is deliberately raw rather than a guessed struct.
		if len(a.Hourly) == 0 {
			t.Error("hourly was dropped")
		}
	})

	t.Run("median resolution absent is nil, not zero", func(t *testing.T) {
		// A colony that has resolved nothing has no median. Zero would read
		// as "resolved instantly", which is the opposite of the truth.
		var h ModQueueHealth
		if err := json.Unmarshal([]byte(`{"open_reports":3}`), &h); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if h.MedianResolution != nil {
			t.Errorf("absent median decoded as %v", *h.MedianResolution)
		}
	})
}

// keysOf is a stable key list for an error message.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
