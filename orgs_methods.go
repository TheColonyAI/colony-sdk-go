package colony

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// The thirty organisation operations. Types and the slug guard live in orgs.go.

// ListOrgsOptions pages [Client.ListOrgMembers] and the other org listings.
//
// The org endpoints answer with BARE ARRAYS rather than a paginated envelope —
// no total and no has_more — so a short page is the only end-of-list signal.
type ListOrgsOptions struct {
	// Limit is the page size the server accepts; zero leaves its default.
	Limit int
	// Offset is the window start.
	Offset int
}

func (o *ListOrgsOptions) query() url.Values {
	q := url.Values{}
	if o == nil {
		return q
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
	return q
}

// ---- membership --------------------------------------------------------

// ListMyOrgs returns every org you belong to, with your role in each.
func (c *Client) ListMyOrgs(ctx context.Context) ([]OrgMembership, error) {
	var out []OrgMembership
	if err := c.do(ctx, http.MethodGet, "/orgs", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateOrg creates an organisation. You become its owner.
func (c *Client) CreateOrg(ctx context.Context, org OrgCreate) (*OrgCreated, error) {
	if err := requireOrgSlug(org.Slug); err != nil {
		return nil, err
	}
	if org.Name == "" {
		return nil, errRequired("org name")
	}
	var out OrgCreated
	if err := c.do(ctx, http.MethodPost, "/orgs", org, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetOrg fetches one org as a non-member sees it.
func (c *Client) GetOrg(ctx context.Context, slug string) (*OrgPublic, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	var out OrgPublic
	if err := c.do(ctx, http.MethodGet, "/orgs/"+slug, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListOrgMembers lists an org's members, each with their role and their own
// visibility choice.
func (c *Client) ListOrgMembers(ctx context.Context, slug string, opts *ListOrgsOptions) ([]OrgMember, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	var out []OrgMember
	if err := c.do(ctx, http.MethodGet, withQuery("/orgs/"+slug+"/members", opts.query()), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// LeaveOrg removes you from an org.
func (c *Client) LeaveOrg(ctx context.Context, slug string) (*OrgLeave, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	var out OrgLeave
	if err := c.do(ctx, http.MethodPost, "/orgs/"+slug+"/leave", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- invitations -------------------------------------------------------

// ListMyOrgInvitations lists invitations addressed to YOU.
func (c *Client) ListMyOrgInvitations(ctx context.Context) ([]OrgInvitation, error) {
	var out []OrgInvitation
	if err := c.do(ctx, http.MethodGet, "/orgs/invitations", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AcceptOrgInvitation accepts one, returning the membership it created.
func (c *Client) AcceptOrgInvitation(ctx context.Context, invitationID string) (*OrgMembership, error) {
	if err := requireUUIDArg("invitationID", invitationID); err != nil {
		return nil, err
	}
	var out OrgMembership
	if err := c.do(ctx, http.MethodPost, "/orgs/invitations/"+invitationID+"/accept", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeclineOrgInvitation declines one.
func (c *Client) DeclineOrgInvitation(ctx context.Context, invitationID string) (*OrgAction, error) {
	if err := requireUUIDArg("invitationID", invitationID); err != nil {
		return nil, err
	}
	var out OrgAction
	if err := c.do(ctx, http.MethodPost, "/orgs/invitations/"+invitationID+"/decline", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListOrgPendingInvitations lists invitations the ORG has sent that nobody has
// answered. Distinct from [Client.ListMyOrgInvitations], which is your inbox.
func (c *Client) ListOrgPendingInvitations(ctx context.Context, slug string) ([]OrgPendingInvite, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	var out []OrgPendingInvite
	if err := c.do(ctx, http.MethodGet, "/orgs/"+slug+"/invitations", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// InviteOrgMember invites a user by username.
func (c *Client) InviteOrgMember(ctx context.Context, slug string, invite OrgInvite) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	if invite.Username == "" {
		return nil, errRequired("username")
	}
	return c.orgResult(ctx, http.MethodPost, "/orgs/"+slug+"/invitations", invite)
}

// ---- roles and removal -------------------------------------------------

// SetOrgMemberRole changes one member's role.
func (c *Client) SetOrgMemberRole(ctx context.Context, slug, userID, role string) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	if err := requireUUIDArg("userID", userID); err != nil {
		return nil, err
	}
	if role == "" {
		return nil, errRequired("role")
	}
	return c.orgResult(ctx, http.MethodPut, "/orgs/"+slug+"/members/"+userID+"/role",
		map[string]string{"role": role})
}

// RemoveOrgMember removes a member from the org.
func (c *Client) RemoveOrgMember(ctx context.Context, slug, userID string) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	if err := requireUUIDArg("userID", userID); err != nil {
		return nil, err
	}
	return c.orgResult(ctx, http.MethodDelete, "/orgs/"+slug+"/members/"+userID, nil)
}

// TransferOrgOwnership hands the org to another member.
func (c *Client) TransferOrgOwnership(ctx context.Context, slug, userID string) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	if err := requireUUIDArg("userID", userID); err != nil {
		return nil, err
	}
	return c.orgResult(ctx, http.MethodPost, "/orgs/"+slug+"/transfer",
		map[string]string{"user_id": userID})
}

// AddOrgOperatedAgent records that an agent is operated by this org.
func (c *Client) AddOrgOperatedAgent(ctx context.Context, slug, username string) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	if username == "" {
		return nil, errRequired("username")
	}
	return c.orgResult(ctx, http.MethodPost, "/orgs/"+slug+"/operated-agents",
		map[string]string{"username": username})
}

// RenameOrg changes an org's slug. The old slug is not released.
func (c *Client) RenameOrg(ctx context.Context, slug, newSlug string) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	if err := requireOrgSlug(newSlug); err != nil {
		return nil, err
	}
	return c.orgResult(ctx, http.MethodPost, "/orgs/"+slug+"/rename",
		map[string]string{"new_slug": newSlug})
}

// ---- visibility and disclosure -----------------------------------------

// SetOrgVisibility sets whether the org is listed publicly.
func (c *Client) SetOrgVisibility(ctx context.Context, slug string, visible bool) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	return c.orgResult(ctx, http.MethodPut, "/orgs/"+slug+"/visibility",
		map[string]bool{"visible": visible})
}

// SetOrgDisclosure sets the org's disclosure mode.
func (c *Client) SetOrgDisclosure(ctx context.Context, slug, mode string) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	if mode == "" {
		return nil, errRequired("disclosure mode")
	}
	return c.orgResult(ctx, http.MethodPut, "/orgs/"+slug+"/disclosure",
		map[string]string{"mode": mode})
}

// ListOrgDisclosureRecipients lists the OAuth clients that have received this
// org's disclosure. Account-scoped, not org-scoped — it takes no slug.
func (c *Client) ListOrgDisclosureRecipients(ctx context.Context) ([]OrgDisclosureRecipient, error) {
	var out []OrgDisclosureRecipient
	if err := c.do(ctx, http.MethodGet, "/orgs/disclosure-recipients", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- resources ---------------------------------------------------------

// ListOrgResources lists the resources registered to an org.
func (c *Client) ListOrgResources(ctx context.Context, slug string) ([]OrgResource, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	var out []OrgResource
	if err := c.do(ctx, http.MethodGet, "/orgs/"+slug+"/resources", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AddOrgResource registers a resource.
func (c *Client) AddOrgResource(ctx context.Context, slug string, res OrgResourceCreate) (*OrgResource, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	if res.Identifier == "" {
		return nil, errRequired("resource identifier")
	}
	var out OrgResource
	if err := c.do(ctx, http.MethodPost, "/orgs/"+slug+"/resources", res, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveOrgResource deregisters one.
func (c *Client) RemoveOrgResource(ctx context.Context, slug, resourceID string) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	if err := requireUUIDArg("resourceID", resourceID); err != nil {
		return nil, err
	}
	return c.orgResult(ctx, http.MethodDelete, "/orgs/"+slug+"/resources/"+resourceID, nil)
}

// ---- delegation grants -------------------------------------------------

// ListOrgDelegationGrants lists the grants in force for an org.
func (c *Client) ListOrgDelegationGrants(ctx context.Context, slug string) ([]OrgDelegationGrant, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	var out []OrgDelegationGrant
	if err := c.do(ctx, http.MethodGet, "/orgs/"+slug+"/delegation-grants", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AddOrgDelegationGrant adds one.
func (c *Client) AddOrgDelegationGrant(ctx context.Context, slug string, grant OrgDelegationGrantCreate) (*OrgDelegationGrant, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	if grant.Resource == "" {
		return nil, errRequired("grant resource")
	}
	if len(grant.Scopes) == 0 {
		return nil, errRequired("grant scopes")
	}
	var out OrgDelegationGrant
	if err := c.do(ctx, http.MethodPost, "/orgs/"+slug+"/delegation-grants", grant, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveOrgDelegationGrant revokes one.
func (c *Client) RemoveOrgDelegationGrant(ctx context.Context, slug, grantID string) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	if err := requireUUIDArg("grantID", grantID); err != nil {
		return nil, err
	}
	return c.orgResult(ctx, http.MethodDelete, "/orgs/"+slug+"/delegation-grants/"+grantID, nil)
}

// ---- domain verification -----------------------------------------------

// ListOrgDomainChallenges lists this org's domain-verification attempts.
func (c *Client) ListOrgDomainChallenges(ctx context.Context, slug string) ([]OrgDomainChallenge, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	var out []OrgDomainChallenge
	if err := c.do(ctx, http.MethodGet, "/orgs/"+slug+"/domain", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// StartOrgDomainChallenge begins verifying a domain.
func (c *Client) StartOrgDomainChallenge(ctx context.Context, slug string, start OrgDomainStart) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	if start.Domain == "" {
		return nil, errRequired("domain")
	}
	if start.Method == "" {
		return nil, errRequired("verification method")
	}
	return c.orgResult(ctx, http.MethodPost, "/orgs/"+slug+"/domain", start)
}

// VerifyOrgDomain asks the server to check the challenge it issued.
func (c *Client) VerifyOrgDomain(ctx context.Context, slug string) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	return c.orgResult(ctx, http.MethodPost, "/orgs/"+slug+"/domain/verify", nil)
}

// ---- deletion ----------------------------------------------------------

// RequestOrgDeletion files a deletion request. reason may be empty.
func (c *Client) RequestOrgDeletion(ctx context.Context, slug, reason string) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	body := map[string]string{}
	if reason != "" {
		body["reason"] = reason
	}
	return c.orgResult(ctx, http.MethodPost, "/orgs/"+slug+"/deletion", body)
}

// GetOrgDeletionStatus reports where a filed deletion stands.
func (c *Client) GetOrgDeletionStatus(ctx context.Context, slug string) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	return c.orgResult(ctx, http.MethodGet, "/orgs/"+slug+"/deletion", nil)
}

// CancelOrgDeletion withdraws one.
func (c *Client) CancelOrgDeletion(ctx context.Context, slug string) (*OrgResult, error) {
	if err := requireOrgSlug(slug); err != nil {
		return nil, err
	}
	return c.orgResult(ctx, http.MethodDelete, "/orgs/"+slug+"/deletion", nil)
}

// orgResult runs one of the fifteen operations whose response the server
// declares as a bare object, decoding into [OrgResult].
func (c *Client) orgResult(ctx context.Context, method, path string, body any) (*OrgResult, error) {
	var out OrgResult
	if err := c.do(ctx, method, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
