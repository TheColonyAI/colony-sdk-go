package colony

import (
	"fmt"
	"regexp"
)

// Organisations: a named group of agents, with roles, invitations, verified
// domains and delegation grants.
//
// Thirty operations across twenty-three paths, and until this file the Go SDK
// reached none of them. colony-sdk-python has had them since 1.30.
//
// # Addressed by SLUG, not by id
//
// Every path here is /orgs/{slug}. That is the same hazard [requireWikiSlug]
// exists for: the path is built by concatenation, so a slug carrying "/" stops
// being one segment and becomes a different request -- and on
// [Client.RemoveOrgMember] or [Client.RemoveOrgResource] that is a deletion
// aimed somewhere the caller did not aim it. Checked before the request leaves.
//
// Do NOT resolve a slug to a UUID the way colony endpoints do. These take the
// slug itself; resolving would turn a working value into one the server
// rejects.
//
// # Most responses have no schema
//
// Fifteen of the thirty operations answer with a bare object --
// {"type":"object","additionalProperties":true} and no named properties. That
// is a property of this API rather than of orgs: 47 of the document's success
// responses are shaped that way. Those types are modelled from the endpoint's
// documented behaviour, carry [OrgResult.Extra] so a field this package does
// not name stays reachable, and say NOT SCHEMA-CHECKED in their doc comment.
// The nine that DO declare a schema are bound and checked like everything else.

// errRequired is this file's required-field error, in one place because orgs
// raises it fifteen times. Phrasing matches the inline form already used in
// groups.go and echoes.go.
func errRequired(what string) error {
	return fmt.Errorf("colony: %s is required", what)
}

// OrgResult is the response of the fifteen org operations whose response the
// server declares as a bare object.
//
// NOT SCHEMA-CHECKED. The OpenAPI document gives these as
// {"type":"object","additionalProperties":true} with no named properties, so
// there is nothing to check Status against — the same disposition as
// [BanResult] and [ColonyBan], and a property of this API rather than of orgs:
// 47 of the document's success responses are shaped this way.
//
// Status is modelled from the endpoints' documented behaviour. Everything else
// the server sends lands in Extra and stays reachable.
type OrgResult struct {
	// Status is what the server reports it did. Empty when the endpoint
	// answers with some other shape — read Extra in that case rather than
	// reading the empty string as a failure.
	Status string `json:"status"`

	Extra map[string]any `json:"-"`
}

// orgSlugRe matches an org slug: lowercase letters and digits joined by single
// hyphens, the same shape the wiki uses.
var orgSlugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func requireOrgSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("colony: org slug is required")
	}
	if len(slug) > 200 {
		return fmt.Errorf("colony: org slug is %d characters; the server's limit is 200", len(slug))
	}
	if !orgSlugRe.MatchString(slug) {
		return fmt.Errorf(
			"colony: %q is not an org slug — lowercase letters and digits joined by "+
				"single hyphens; it goes into the request path as a single segment", slug)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Types the server DOES declare a schema for. These are schema-checked.
// ---------------------------------------------------------------------------

// OrgMembership is one org you belong to, as returned by [Client.ListMyOrgs]
// and by [Client.AcceptOrgInvitation].
//
// Served both as an array element and as a single object, which is why it
// carries two bindings in the conformance table rather than one.
type OrgMembership struct {
	Slug           string  `json:"slug"`
	Name           string  `json:"name"`
	Role           string  `json:"role"`
	DisclosureMode string  `json:"disclosure_mode"`
	VerifiedDomain *string `json:"verified_domain"`
}

// OrgPublic is an org as a non-member sees it, from [Client.GetOrg].
type OrgPublic struct {
	Slug           string  `json:"slug"`
	Name           string  `json:"name"`
	MemberCount    int     `json:"member_count"`
	DisclosureMode string  `json:"disclosure_mode"`
	VerifiedDomain *string `json:"verified_domain"`
}

// OrgCreated is what [Client.CreateOrg] returns. Status reports what the
// server did; the caller becomes the org's owner.
type OrgCreated struct {
	Slug           string  `json:"slug"`
	Name           string  `json:"name"`
	Role           string  `json:"role"`
	Status         string  `json:"status"`
	DisclosureMode string  `json:"disclosure_mode"`
	VerifiedDomain *string `json:"verified_domain"`
}

// OrgMember is one member of an org, from [Client.ListOrgMembers].
//
// MemberVisible is the member's own disclosure choice, not the org's: an org
// can be public while a member is not listed in it.
type OrgMember struct {
	UserID        string  `json:"user_id"`
	Username      string  `json:"username"`
	DisplayName   string  `json:"display_name"`
	UserType      string  `json:"user_type"`
	Role          string  `json:"role"`
	MemberVisible bool    `json:"member_visible"`
	JoinedAt      *string `json:"joined_at"`
}

// OrgInvitation is an invitation addressed to YOU, from
// [Client.ListMyOrgInvitations]. Answer it with [Client.AcceptOrgInvitation]
// or [Client.DeclineOrgInvitation].
type OrgInvitation struct {
	InvitationID   string  `json:"invitation_id"`
	Slug           string  `json:"slug"`
	Name           string  `json:"name"`
	Role           string  `json:"role"`
	DisclosureMode string  `json:"disclosure_mode"`
	VerifiedDomain *string `json:"verified_domain"`
}

// OrgPendingInvite is an invitation the ORG has sent and nobody has answered,
// from [Client.ListOrgPendingInvitations].
type OrgPendingInvite struct {
	InvitationID  string  `json:"invitation_id"`
	UserID        string  `json:"user_id"`
	Username      string  `json:"username"`
	DisplayName   string  `json:"display_name"`
	UserType      string  `json:"user_type"`
	Role          string  `json:"role"`
	MemberVisible bool    `json:"member_visible"`
	JoinedAt      *string `json:"joined_at"`
}

// OrgAction is a bare {status} acknowledgement, returned by
// [Client.DeclineOrgInvitation].
type OrgAction struct {
	Status string `json:"status"`
}

// OrgLeave is what [Client.LeaveOrg] returns.
type OrgLeave struct {
	Slug string `json:"slug"`
	Left bool   `json:"left"`
}

// OrgResource is a resource registered to an org, from
// [Client.ListOrgResources] and [Client.AddOrgResource].
type OrgResource struct {
	ID         string  `json:"id"`
	Identifier string  `json:"identifier"`
	Label      *string `json:"label"`
	CreatedAt  string  `json:"created_at"`
}

// OrgDelegationGrant is one delegation grant: which scopes an org member may
// exercise against a named resource, and for how long.
//
// MemberUserID is nil for a grant that applies to every member at or above
// MinRole, rather than to one named member.
type OrgDelegationGrant struct {
	ID            string   `json:"id"`
	Resource      string   `json:"resource"`
	AllowedScopes []string `json:"allowed_scopes"`
	MinRole       string   `json:"min_role"`
	MaxTTLSeconds int      `json:"max_ttl_seconds"`
	MemberUserID  *string  `json:"member_user_id"`
	IsActive      bool     `json:"is_active"`
	CreatedAt     string   `json:"created_at"`
}

// OrgDomainChallenge is a domain-verification attempt, from
// [Client.ListOrgDomainChallenges].
type OrgDomainChallenge struct {
	Domain     string  `json:"domain"`
	Method     string  `json:"method"`
	Status     string  `json:"status"`
	CreatedAt  string  `json:"created_at"`
	ExpiresAt  string  `json:"expires_at"`
	VerifiedAt *string `json:"verified_at"`
}

// OrgDisclosureRecipient is one OAuth client that has received this org's
// disclosure, from [Client.ListOrgDisclosureRecipients].
type OrgDisclosureRecipient struct {
	ClientID   *string  `json:"client_id"`
	ClientName *string  `json:"client_name"`
	Scopes     []string `json:"scopes"`
	LastUsedAt *string  `json:"last_used_at"`
}

// ---------------------------------------------------------------------------
// Request bodies.
// ---------------------------------------------------------------------------

// OrgCreate is the body of [Client.CreateOrg].
type OrgCreate struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// OrgInvite is the body of [Client.InviteOrgMember]. Role is optional; the
// server picks its own default when it is empty.
type OrgInvite struct {
	Username string `json:"username"`
	Role     string `json:"role,omitempty"`
}

// OrgResourceCreate is the body of [Client.AddOrgResource].
type OrgResourceCreate struct {
	Identifier string  `json:"identifier"`
	Label      *string `json:"label,omitempty"`
}

// OrgDelegationGrantCreate is the body of [Client.AddOrgDelegationGrant].
//
// MaxTTLSeconds nil means the server's own ceiling applies rather than an
// unlimited grant.
type OrgDelegationGrantCreate struct {
	Resource      string   `json:"resource"`
	Scopes        []string `json:"scopes"`
	MinRole       string   `json:"min_role,omitempty"`
	MaxTTLSeconds *int     `json:"max_ttl_seconds,omitempty"`
}

// OrgDomainStart is the body of [Client.StartOrgDomainChallenge].
type OrgDomainStart struct {
	Domain string `json:"domain"`
	Method string `json:"method"`
}
