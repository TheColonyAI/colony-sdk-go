package colony

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type orgRec struct {
	method string
	path   string
	query  string
	body   []byte
}

// orgServer stubs the API. It reads the request body with io.ReadAll for the
// reason wiki_test.go records: a single Read may return a prefix, and every
// assertion about rec.body would then be conditional on the body being small.
func orgServer(t *testing.T, status int, reply string) (*Client, *orgRec) {
	t.Helper()
	rec := &orgRec{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		rec.method, rec.path, rec.query = r.Method, r.URL.Path, r.URL.RawQuery
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("stub: reading request body: %v", err)
			}
			rec.body = b
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

const orgUUID = "11111111-1111-4111-8111-111111111111"

// ---- the slug guard ----------------------------------------------------

// TestOrgSlugGuardRefusesBeforeTheRequestLeaves is the one that matters most.
// Every org path is built by concatenation, so a slug carrying "/" stops being
// one segment — and on RemoveOrgMember that is a deletion aimed somewhere the
// caller did not aim it. The guard must refuse WITHOUT issuing a request.
func TestOrgSlugGuardRefusesBeforeTheRequestLeaves(t *testing.T) {
	bad := []string{
		"",
		"has/slash",
		"../escape",
		"Upper",
		"trailing-",
		"double--hyphen-is-fine-actually/no",
		strings.Repeat("a", 201),
	}
	for _, slug := range bad {
		c, rec := orgServer(t, 200, `{"status":"ok"}`)
		if _, err := c.GetOrg(context.Background(), slug); err == nil {
			t.Errorf("GetOrg(%q): expected refusal, got nil", slug)
		}
		if rec.method != "" {
			t.Errorf("GetOrg(%q): a request was ISSUED (%s %s) — the guard must "+
				"refuse before anything leaves", slug, rec.method, rec.path)
		}
	}
}

// The must-pass arm. A guard that refuses everything is not a guard, it is an
// outage, and a refusal-only test cannot tell the two apart.
func TestOrgSlugGuardLetsRealSlugsThrough(t *testing.T) {
	for _, slug := range []string{"acme", "acme-labs", "a1", "a-b-c-9"} {
		c, rec := orgServer(t, 200, `{"slug":"acme","name":"Acme","member_count":2,"disclosure_mode":"public"}`)
		if _, err := c.GetOrg(context.Background(), slug); err != nil {
			t.Errorf("GetOrg(%q): unexpected refusal: %v", slug, err)
		}
		if rec.path != "/orgs/"+slug {
			t.Errorf("GetOrg(%q): path = %q", slug, rec.path)
		}
	}
}

// The same guard on the DESTRUCTIVE calls, named separately because these are
// the ones where a bad slug retargets the deletion.
func TestOrgSlugGuardOnDestructiveCalls(t *testing.T) {
	c, rec := orgServer(t, 200, `{"status":"ok"}`)
	ctx := context.Background()
	if _, err := c.RemoveOrgMember(ctx, "bad/slug", orgUUID); err == nil {
		t.Error("RemoveOrgMember: expected refusal on a slug with a slash")
	}
	if _, err := c.RemoveOrgResource(ctx, "bad/slug", orgUUID); err == nil {
		t.Error("RemoveOrgResource: expected refusal")
	}
	if _, err := c.RemoveOrgDelegationGrant(ctx, "bad/slug", orgUUID); err == nil {
		t.Error("RemoveOrgDelegationGrant: expected refusal")
	}
	if rec.method != "" {
		t.Errorf("a destructive request ESCAPED the guard: %s %s", rec.method, rec.path)
	}
}

// The id arguments are UUIDs and go into the path too.
func TestOrgIDArgsAreChecked(t *testing.T) {
	c, rec := orgServer(t, 200, `{"status":"ok"}`)
	ctx := context.Background()
	if _, err := c.RemoveOrgMember(ctx, "acme", "not-a-uuid"); err == nil {
		t.Error("RemoveOrgMember: expected refusal on a non-UUID userID")
	}
	if _, err := c.AcceptOrgInvitation(ctx, "../escape"); err == nil {
		t.Error("AcceptOrgInvitation: expected refusal on a non-UUID id")
	}
	if rec.method != "" {
		t.Errorf("a request escaped the id guard: %s %s", rec.method, rec.path)
	}
}

// ---- required fields ---------------------------------------------------

func TestOrgRequiredFieldsRefuseLocally(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func(*Client) error
	}{
		{"CreateOrg no name", func(c *Client) error {
			_, err := c.CreateOrg(ctx, OrgCreate{Slug: "acme"})
			return err
		}},
		{"InviteOrgMember no username", func(c *Client) error {
			_, err := c.InviteOrgMember(ctx, "acme", OrgInvite{})
			return err
		}},
		{"SetOrgMemberRole no role", func(c *Client) error {
			_, err := c.SetOrgMemberRole(ctx, "acme", orgUUID, "")
			return err
		}},
		{"AddOrgResource no identifier", func(c *Client) error {
			_, err := c.AddOrgResource(ctx, "acme", OrgResourceCreate{})
			return err
		}},
		{"AddOrgDelegationGrant no scopes", func(c *Client) error {
			_, err := c.AddOrgDelegationGrant(ctx, "acme", OrgDelegationGrantCreate{Resource: "r"})
			return err
		}},
		{"StartOrgDomainChallenge no domain", func(c *Client) error {
			_, err := c.StartOrgDomainChallenge(ctx, "acme", OrgDomainStart{Method: "dns"})
			return err
		}},
	}
	for _, tc := range cases {
		c, rec := orgServer(t, 200, `{"status":"ok"}`)
		if err := tc.call(c); err == nil {
			t.Errorf("%s: expected a local refusal", tc.name)
		}
		if rec.method != "" {
			t.Errorf("%s: request issued despite a missing required field", tc.name)
		}
	}
}

// ---- wire shapes -------------------------------------------------------

func TestListMyOrgsDecodesABareArray(t *testing.T) {
	c, rec := orgServer(t, 200, `[
	  {"slug":"acme","name":"Acme","role":"owner","disclosure_mode":"public","verified_domain":"acme.com"},
	  {"slug":"beta","name":"Beta","role":"member","disclosure_mode":"private","verified_domain":null}
	]`)
	got, err := c.ListMyOrgs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodGet || rec.path != "/orgs" {
		t.Errorf("request = %s %s", rec.method, rec.path)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Role != "owner" || got[0].VerifiedDomain == nil || *got[0].VerifiedDomain != "acme.com" {
		t.Errorf("first row decoded wrong: %+v", got[0])
	}
	// A null verified_domain must stay nil, not become "".
	if got[1].VerifiedDomain != nil {
		t.Errorf("null verified_domain must decode to nil, got %q", *got[1].VerifiedDomain)
	}
}

func TestListOrgMembersSendsItsWindow(t *testing.T) {
	c, rec := orgServer(t, 200, `[]`)
	if _, err := c.ListOrgMembers(context.Background(), "acme", &ListOrgsOptions{Limit: 25, Offset: 50}); err != nil {
		t.Fatal(err)
	}
	if rec.path != "/orgs/acme/members" {
		t.Errorf("path = %q", rec.path)
	}
	if !strings.Contains(rec.query, "limit=25") || !strings.Contains(rec.query, "offset=50") {
		t.Errorf("query = %q, want limit and offset", rec.query)
	}
}

// A nil options struct must not panic and must send no window at all.
func TestListOrgMembersNilOptions(t *testing.T) {
	c, rec := orgServer(t, 200, `[]`)
	if _, err := c.ListOrgMembers(context.Background(), "acme", nil); err != nil {
		t.Fatal(err)
	}
	if rec.query != "" {
		t.Errorf("nil options must send no query, got %q", rec.query)
	}
}

func TestCreateOrgSendsTheBody(t *testing.T) {
	c, rec := orgServer(t, 201, `{"slug":"acme","name":"Acme","role":"owner","status":"created","disclosure_mode":"public"}`)
	desc := "a description"
	got, err := c.CreateOrg(context.Background(), OrgCreate{Slug: "acme", Name: "Acme", Description: &desc})
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("body was not JSON: %v (%q)", err, rec.body)
	}
	if sent["slug"] != "acme" || sent["name"] != "Acme" || sent["description"] != "a description" {
		t.Errorf("sent = %v", sent)
	}
	if got.Status != "created" || got.Role != "owner" {
		t.Errorf("decoded = %+v", got)
	}
}

// An omitted description must be ABSENT from the body, not sent as null — the
// server distinguishes "unset" from "cleared".
func TestCreateOrgOmitsAnUnsetDescription(t *testing.T) {
	c, rec := orgServer(t, 201, `{"slug":"acme","name":"Acme","role":"owner","status":"created","disclosure_mode":"public"}`)
	if _, err := c.CreateOrg(context.Background(), OrgCreate{Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	_ = json.Unmarshal(rec.body, &sent)
	if _, present := sent["description"]; present {
		t.Errorf("an unset description must be omitted, got body %q", rec.body)
	}
}

// ---- the bare-object responses ----------------------------------------

// TestOrgResultKeepsWhatItDoesNotName is why OrgResult has an UnmarshalJSON.
// The server declares these fifteen responses as bare objects, so Extra is not
// an occasional extra field — it is most of the response. Without the method,
// Extra is tagged json:"-", the decoder skips it, and it is nil on every call.
func TestOrgResultKeepsWhatItDoesNotName(t *testing.T) {
	c, _ := orgServer(t, 200, `{"status":"invited","invitation_id":"abc","expires_in":86400}`)
	got, err := c.InviteOrgMember(context.Background(), "acme", OrgInvite{Username: "someone"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "invited" {
		t.Errorf("Status = %q", got.Status)
	}
	if got.Extra == nil {
		t.Fatal("Extra is nil — the UnmarshalJSON is missing and the rest of " +
			"the response was silently discarded")
	}
	if got.Extra["invitation_id"] != "abc" {
		t.Errorf("Extra[invitation_id] = %v", got.Extra["invitation_id"])
	}
	if got.Extra["expires_in"].(float64) != 86400 {
		t.Errorf("Extra[expires_in] = %v", got.Extra["expires_in"])
	}
	// A field the struct DOES name must not be duplicated into Extra.
	if _, dup := got.Extra["status"]; dup {
		t.Error("a modelled field must not also appear in Extra")
	}
}

// ---- routes ------------------------------------------------------------

// Every method's METHOD and PATH, asserted per branch. The cheapest test there
// is, and the one that catches a copy-paste that sends a PUT to the wrong
// endpoint — which is how GroupInviteResponse came to be tagged wrong.
func TestOrgRoutes(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		call       func(*Client) error
		wantMethod string
		wantPath   string
		// reply is this case's stub body. Per-case rather than shared: the
		// seven listing methods decode a bare ARRAY and the rest decode an
		// object, so one reply for all of them cannot be right for both.
		reply string
	}{
		{
			name: "ListMyOrgs", call: func(c *Client) error { _, e := c.ListMyOrgs(ctx); return e },
			wantMethod: http.MethodGet, wantPath: "/orgs",
			reply: "[]",
		},
		{
			name: "GetOrg", call: func(c *Client) error { _, e := c.GetOrg(ctx, "acme"); return e },
			wantMethod: http.MethodGet, wantPath: "/orgs/acme",
		},
		{
			name: "LeaveOrg", call: func(c *Client) error { _, e := c.LeaveOrg(ctx, "acme"); return e },
			wantMethod: http.MethodPost, wantPath: "/orgs/acme/leave",
		},
		{
			name: "ListMyOrgInvitations", call: func(c *Client) error { _, e := c.ListMyOrgInvitations(ctx); return e },
			wantMethod: http.MethodGet, wantPath: "/orgs/invitations",
			reply: "[]",
		},
		{
			name: "AcceptOrgInvitation", call: func(c *Client) error { _, e := c.AcceptOrgInvitation(ctx, orgUUID); return e },
			wantMethod: http.MethodPost, wantPath: "/orgs/invitations/" + orgUUID + "/accept",
		},
		{
			name: "DeclineOrgInvitation", call: func(c *Client) error { _, e := c.DeclineOrgInvitation(ctx, orgUUID); return e },
			wantMethod: http.MethodPost, wantPath: "/orgs/invitations/" + orgUUID + "/decline",
		},
		{
			name: "ListOrgPendingInvitations", call: func(c *Client) error { _, e := c.ListOrgPendingInvitations(ctx, "acme"); return e },
			wantMethod: http.MethodGet, wantPath: "/orgs/acme/invitations",
			reply: "[]",
		},
		{
			name: "SetOrgMemberRole", call: func(c *Client) error { _, e := c.SetOrgMemberRole(ctx, "acme", orgUUID, "admin"); return e },
			wantMethod: http.MethodPut, wantPath: "/orgs/acme/members/" + orgUUID + "/role",
		},
		{
			name: "RemoveOrgMember", call: func(c *Client) error { _, e := c.RemoveOrgMember(ctx, "acme", orgUUID); return e },
			wantMethod: http.MethodDelete, wantPath: "/orgs/acme/members/" + orgUUID,
		},
		{
			name: "TransferOrgOwnership", call: func(c *Client) error { _, e := c.TransferOrgOwnership(ctx, "acme", orgUUID); return e },
			wantMethod: http.MethodPost, wantPath: "/orgs/acme/transfer",
		},
		{
			name: "AddOrgOperatedAgent", call: func(c *Client) error { _, e := c.AddOrgOperatedAgent(ctx, "acme", "bot"); return e },
			wantMethod: http.MethodPost, wantPath: "/orgs/acme/operated-agents",
		},
		{
			name: "RenameOrg", call: func(c *Client) error { _, e := c.RenameOrg(ctx, "acme", "acme-labs"); return e },
			wantMethod: http.MethodPost, wantPath: "/orgs/acme/rename",
		},
		{
			name: "SetOrgVisibility", call: func(c *Client) error { _, e := c.SetOrgVisibility(ctx, "acme", true); return e },
			wantMethod: http.MethodPut, wantPath: "/orgs/acme/visibility",
		},
		{
			name: "SetOrgDisclosure", call: func(c *Client) error { _, e := c.SetOrgDisclosure(ctx, "acme", "members"); return e },
			wantMethod: http.MethodPut, wantPath: "/orgs/acme/disclosure",
		},
		{
			name: "ListOrgDisclosureRecipients", call: func(c *Client) error { _, e := c.ListOrgDisclosureRecipients(ctx); return e },
			wantMethod: http.MethodGet, wantPath: "/orgs/disclosure-recipients",
			reply: "[]",
		},
		{
			name: "ListOrgResources", call: func(c *Client) error { _, e := c.ListOrgResources(ctx, "acme"); return e },
			wantMethod: http.MethodGet, wantPath: "/orgs/acme/resources",
			reply: "[]",
		},
		{
			name: "RemoveOrgResource", call: func(c *Client) error { _, e := c.RemoveOrgResource(ctx, "acme", orgUUID); return e },
			wantMethod: http.MethodDelete, wantPath: "/orgs/acme/resources/" + orgUUID,
		},
		{
			name: "ListOrgDelegationGrants", call: func(c *Client) error { _, e := c.ListOrgDelegationGrants(ctx, "acme"); return e },
			wantMethod: http.MethodGet, wantPath: "/orgs/acme/delegation-grants",
			reply: "[]",
		},
		{
			name: "RemoveOrgDelegationGrant", call: func(c *Client) error { _, e := c.RemoveOrgDelegationGrant(ctx, "acme", orgUUID); return e },
			wantMethod: http.MethodDelete, wantPath: "/orgs/acme/delegation-grants/" + orgUUID,
		},
		{
			name: "ListOrgDomainChallenges", call: func(c *Client) error { _, e := c.ListOrgDomainChallenges(ctx, "acme"); return e },
			wantMethod: http.MethodGet, wantPath: "/orgs/acme/domain",
			reply: "[]",
		},
		{
			name: "VerifyOrgDomain", call: func(c *Client) error { _, e := c.VerifyOrgDomain(ctx, "acme"); return e },
			wantMethod: http.MethodPost, wantPath: "/orgs/acme/domain/verify",
		},
		{
			name: "RequestOrgDeletion", call: func(c *Client) error { _, e := c.RequestOrgDeletion(ctx, "acme", "done"); return e },
			wantMethod: http.MethodPost, wantPath: "/orgs/acme/deletion",
		},
		{
			name: "GetOrgDeletionStatus", call: func(c *Client) error { _, e := c.GetOrgDeletionStatus(ctx, "acme"); return e },
			wantMethod: http.MethodGet, wantPath: "/orgs/acme/deletion",
		},
		{
			name: "CancelOrgDeletion", call: func(c *Client) error { _, e := c.CancelOrgDeletion(ctx, "acme"); return e },
			wantMethod: http.MethodDelete, wantPath: "/orgs/acme/deletion",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := tc.reply
			if reply == "" {
				reply = `{"status":"ok"}`
			}
			c, rec := orgServer(t, 200, reply)
			if err := tc.call(c); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if rec.method != tc.wantMethod || rec.path != tc.wantPath {
				t.Errorf("%s: got %s %s, want %s %s",
					tc.name, rec.method, rec.path, tc.wantMethod, tc.wantPath)
			}
		})
	}
}

// RequestOrgDeletion with no reason must omit the field rather than send "".
func TestRequestOrgDeletionOmitsAnEmptyReason(t *testing.T) {
	c, rec := orgServer(t, 200, `{"status":"pending"}`)
	if _, err := c.RequestOrgDeletion(context.Background(), "acme", ""); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	_ = json.Unmarshal(rec.body, &sent)
	if _, present := sent["reason"]; present {
		t.Errorf("an empty reason must be omitted, got %q", rec.body)
	}
}
