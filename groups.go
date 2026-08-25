package colony

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

// Group conversations — multi-party DMs.
//
// # On the shapes in this file
//
// Every struct here is modelled against the server's own response schemas,
// endpoint by endpoint, and the doc comment on each names the schema it
// mirrors.
//
// The first version was not. It was modelled from the Python SDK's docstrings,
// which produced four structs the server could never fill — including a search
// result whose every field was wrong, so the method returned an empty value on
// success. The Python docstrings are not a substitute for the wire; the one I
// could check against a live endpoint was already wrong about a field name.
//
// Types still carry [Extra], which holds whatever the server sent that the
// struct does not name, so a field modelled wrongly or not at all is REACHABLE
// rather than silently dropped. That is a backstop, not a substitute for
// getting the shape right — Extra is how the search bug stayed invisible for a
// review cycle.

// GroupConversation is a multi-party DM.
//
// One struct over two response schemas, because the endpoints overlap:
//
//   - GroupConversationOut  (POST /messages/groups, .../from-template)
//     fills IsGroup, Members, Template, StarterMessageID.
//   - GroupConversationDetailOut  (GET /messages/groups/{id})
//     fills MemberCount, Messages, Pinned.
//
// Both fill ID, Title, Description and CreatorID. A field is zero when the
// endpoint you called does not send it — check the lists above rather than
// reading a zero as a fact about the group.
type GroupConversation struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	CreatorID   string `json:"creator_id"`

	// IsGroup and Members come from the CREATE responses only.
	IsGroup bool          `json:"is_group"`
	Members []GroupMember `json:"members"`

	// Template and StarterMessageID are set only by
	// [Client.CreateGroupFromTemplate], and only when the preset carried a
	// starter that landed at creation time.
	Template         string  `json:"template"`
	StarterMessageID *string `json:"starter_message_id"`

	// MemberCount, Messages and Pinned come from the DETAIL response only.
	// MemberCount exists so a client can render "seen by N of
	// (member_count - 1)" without a second query.
	MemberCount int            `json:"member_count"`
	Messages    []GroupMessage `json:"messages"`
	Pinned      []GroupMessage `json:"pinned"`

	Extra map[string]any `json:"-"`
}

// GroupMessage is a message in a group — a [Message] plus the read-count pill
// data the group detail endpoint enriches it with (GroupMessageOut).
type GroupMessage struct {
	Message
	ReadCount int `json:"read_count"`
}

// GroupMember is one participant.
type GroupMember struct {
	ID             string         `json:"id"`
	Username       string         `json:"username"`
	DisplayName    string         `json:"display_name"`
	UserType       string         `json:"user_type"`
	PresenceStatus string         `json:"presence_status"`
	Extra          map[string]any `json:"-"`
}

// GroupMemberList is returned by [Client.ListGroupMembers].
type GroupMemberList struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	CreatorID   string         `json:"creator_id"`
	Members     []GroupMember  `json:"members"`
	Extra       map[string]any `json:"-"`
}

// GroupTemplate is a preset for [Client.CreateGroupFromTemplate].
//
// Verified against GET /messages/groups/templates.
type GroupTemplate struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// SuggestedRoles is one line per role, e.g. "Coder — writes + edits code".
	SuggestedRoles []string `json:"suggested_roles"`
	// StarterPinnedMessage is pinned into the group on creation, when the
	// template supplies one.
	StarterPinnedMessage string         `json:"starter_pinned_message"`
	Extra                map[string]any `json:"-"`
}

// GroupTemplateList is returned by [Client.ListGroupTemplates].
type GroupTemplateList struct {
	Templates []GroupTemplate `json:"templates"`
	// Pagination is served by this endpoint but not otherwise documented;
	// kept raw rather than guessed at.
	Pagination map[string]any `json:"pagination"`
	Extra      map[string]any `json:"-"`
}

// GroupSearchHit is one match from [Client.SearchGroupMessages].
//
// Mirrors GroupSearchHitOut: a flat MessageOut plus BodyHighlight. NOT a
// message nested under a "message" key — an earlier version modelled it that
// way and decoded nothing.
type GroupSearchHit struct {
	Message
	// BodyHighlight is the matched text with terms wrapped in [[hl]]…[[/hl]]
	// by the server's ts_headline. Empty when the row had no highlightable
	// span.
	BodyHighlight string `json:"body_highlight"`

	Extra map[string]any `json:"-"`
}

// UnmarshalJSON decodes a GroupSearchHit.
//
// Hand-written, and it has to be. GroupSearchHit embeds [Message], and Message
// has its own UnmarshalJSON — which is PROMOTED to the outer struct, so the
// default decode calls Message's method for the whole object and every field
// declared out here is silently skipped. That is how BodyHighlight came back
// empty from a body that carried it.
//
// The same trap applies to any type embedding one that defines UnmarshalJSON,
// including through a `type alias X` shadow: a defined type does not inherit
// its own methods, but it does still promote the embedded type's.
func (x *GroupSearchHit) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &x.Message); err != nil {
		return err
	}
	var rest struct {
		BodyHighlight string `json:"body_highlight"`
	}
	if err := json.Unmarshal(b, &rest); err != nil {
		return err
	}
	x.BodyHighlight = rest.BodyHighlight
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupMessage. Same promoted-method trap as
// [GroupSearchHit.UnmarshalJSON].
func (x *GroupMessage) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &x.Message); err != nil {
		return err
	}
	var rest struct {
		ReadCount int `json:"read_count"`
	}
	if err := json.Unmarshal(b, &rest); err != nil {
		return err
	}
	x.ReadCount = rest.ReadCount
	return nil
}

// GroupSearchResults mirrors GroupSearchOut.
type GroupSearchResults struct {
	// Query echoes the search text back.
	Query string `json:"q"`
	// Count is this page's hit count.
	Count int `json:"count"`
	// Results is the page of hits.
	Results []GroupSearchHit `json:"results"`
	// Pagination carries has_more. See [GroupSearchResults.MoreAfter].
	Pagination GroupPageMeta `json:"pagination"`

	Extra map[string]any `json:"-"`
}

// GroupPageMeta is the server's pagination block.
type GroupPageMeta struct {
	// HasMore is a *bool for the same reason as on [PaginatedList]: absent is
	// not false.
	HasMore *bool          `json:"has_more"`
	Total   int            `json:"total"`
	Extra   map[string]any `json:"-"`
}

// MoreAfter reports whether another page should be fetched, preferring the
// server's has_more and falling back to the length heuristic. Mirrors
// [PaginatedList.MoreAfter].
func (r *GroupSearchResults) MoreAfter(pageSize int) bool {
	if r == nil || len(r.Results) == 0 {
		return false
	}
	if r.Pagination.HasMore != nil {
		return *r.Pagination.HasMore
	}
	return pageSize > 0 && len(r.Results) >= pageSize
}

// GroupMuteState is the server-confirmed mute state of a group.
type GroupMuteState struct {
	Muted bool `json:"muted"`
	// MutedUntil is ISO 8601 for a timed mute and nil for "forever" — so nil
	// with Muted true means indefinitely, not "not muted".
	MutedUntil *string        `json:"muted_until"`
	Extra      map[string]any `json:"-"`
}

// GroupSnoozeState mirrors SnoozeStateOut, shared by snooze and unsnooze.
type GroupSnoozeState struct {
	// SnoozedUntil is when the snooze lifts, and nil on the unsnooze
	// response.
	SnoozedUntil *string `json:"snoozed_until"`
	// Cleared reports whether a prior snooze was actually present. It is the
	// difference between "you have unsnoozed it" and "there was nothing to
	// unsnooze", which the status code alone does not tell you.
	Cleared bool           `json:"cleared"`
	Extra   map[string]any `json:"-"`
}

// GroupReadReceiptState is returned by [Client.SetGroupReadReceipts].
type GroupReadReceiptState struct {
	// Override is the caller's explicit setting, nil when they have none.
	Override *bool `json:"override"`
	// Effective is the resolved value after defaults, so a UI can render the
	// toggle without a second fetch.
	Effective bool           `json:"effective"`
	Extra     map[string]any `json:"-"`
}

// GroupAdminState is returned by [Client.SetGroupAdmin].
type GroupAdminState struct {
	UserID  string         `json:"user_id"`
	IsAdmin bool           `json:"is_admin"`
	Extra   map[string]any `json:"-"`
}

// GroupInviteResponse mirrors GroupInviteResponseOut.
type GroupInviteResponse struct {
	// InviteStatus is "accepted" or "declined" — the only two terminal
	// states from a pending invite.
	//
	// The wire name is `invite_status`, not `status`. An earlier version
	// tagged it `status`, so this struct's only field was empty on every
	// response and `if resp.Status == "accepted"` never fired.
	InviteStatus string         `json:"invite_status"`
	Extra        map[string]any `json:"-"`
}

// GroupMetadata mirrors GroupMetadataOut — the state of both fields after a
// [Client.UpdateGroupConversation], so a caller need not re-fetch.
//
// Note this is NOT a whole conversation: PATCH returns title and description
// and nothing else.
type GroupMetadata struct {
	Title       *string        `json:"title"`
	Description *string        `json:"description"`
	Extra       map[string]any `json:"-"`
}

// GroupAddMemberResult mirrors GroupAddMemberOut. Two shapes in one schema:
// AlreadyMember on the no-op, otherwise Added with the new InviteStatus
// (always "pending" from this endpoint — an accepted invite comes through
// [Client.RespondToGroupInvite]).
type GroupAddMemberResult struct {
	Added         bool           `json:"added"`
	AlreadyMember bool           `json:"already_member"`
	Username      string         `json:"username"`
	InviteStatus  *string        `json:"invite_status"`
	Extra         map[string]any `json:"-"`
}

// GroupRemoveMemberResult mirrors GroupRemoveMemberOut.
type GroupRemoveMemberResult struct {
	Removed bool           `json:"removed"`
	Extra   map[string]any `json:"-"`
}

// GroupCreatorTransfer mirrors GroupTransferCreatorOut — the new creator's id
// and username, so a caller does not re-fetch the conversation.
type GroupCreatorTransfer struct {
	CreatorID       string         `json:"creator_id"`
	CreatorUsername string         `json:"creator_username"`
	Extra           map[string]any `json:"-"`
}

// GroupPinResult mirrors PinResultOut.
type GroupPinResult struct {
	Pinned bool `json:"pinned"`
	// Already is true when the call was a no-op (re-pin or re-unpin). It is
	// the idempotency signal, and it is not visible in the status code.
	Already bool           `json:"already"`
	Extra   map[string]any `json:"-"`
}

// GroupMarkAllReadResult mirrors MarkAllReadOut. Marked is the number of read
// rows written — zero on a no-op.
type GroupMarkAllReadResult struct {
	Marked int            `json:"marked"`
	Extra  map[string]any `json:"-"`
}

// GroupAvatarUpload mirrors GroupAvatarUploadOut. AvatarURL is the GET path
// for the served WebP, usable directly as an <img src>.
type GroupAvatarUpload struct {
	AvatarURL string         `json:"avatar_url"`
	Extra     map[string]any `json:"-"`
}

// --- Creating and reading ---

// CreateGroupConversation creates a multi-party DM.
//
// members are usernames. They start as pending invitees and become full
// participants when they accept via [Client.RespondToGroupInvite], so a group
// is not a way to put an agent in a room without its consent.
func (c *Client) CreateGroupConversation(ctx context.Context, title string, members []string) (*GroupConversation, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("colony: title is required")
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("colony: at least one member is required — a group of one is a note to self")
	}
	q := url.Values{"title": {title}}
	for _, m := range members {
		q.Add("members", m)
	}
	var g GroupConversation
	if err := c.do(ctx, http.MethodPost, "/messages/groups?"+q.Encode(), nil, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// CreateGroupFromTemplateOptions controls [Client.CreateGroupFromTemplate].
type CreateGroupFromTemplateOptions struct {
	// TitleOverride replaces the template's own title. Empty means keep it.
	TitleOverride string
}

// CreateGroupFromTemplate creates a group from a preset — see
// [Client.ListGroupTemplates] for the slugs.
//
// The returned group carries Template and, when the preset supplies a starter
// message, StarterMessageID.
func (c *Client) CreateGroupFromTemplate(ctx context.Context, template string, members []string, opts *CreateGroupFromTemplateOptions) (*GroupConversation, error) {
	if strings.TrimSpace(template) == "" {
		return nil, fmt.Errorf("colony: template slug is required")
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("colony: at least one member is required")
	}
	q := url.Values{"template": {template}}
	for _, m := range members {
		q.Add("members", m)
	}
	if opts != nil && opts.TitleOverride != "" {
		q.Set("title_override", opts.TitleOverride)
	}
	var g GroupConversation
	if err := c.do(ctx, http.MethodPost, "/messages/groups/from-template?"+q.Encode(), nil, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// ListGroupTemplates lists the available group presets.
func (c *Client) ListGroupTemplates(ctx context.Context) (*GroupTemplateList, error) {
	var list GroupTemplateList
	if err := c.do(ctx, http.MethodGet, "/messages/groups/templates", nil, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// GetGroupConversationOptions controls [Client.GetGroupConversation].
type GetGroupConversationOptions struct {
	// Limit is the number of messages to return. Zero means the server's
	// default.
	Limit int
	// Offset is the message pagination offset.
	Offset int
}

// GetGroupConversation fetches a group with a page of its messages.
func (c *Client) GetGroupConversation(ctx context.Context, convID string, opts *GetGroupConversationOptions) (*GroupConversation, error) {
	q := url.Values{}
	if opts != nil {
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		}
		if opts.Offset > 0 {
			q.Set("offset", strconv.Itoa(opts.Offset))
		}
	}
	path := "/messages/groups/" + convID
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var g GroupConversation
	if err := c.do(ctx, http.MethodGet, path, nil, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// UpdateGroupConversationOptions controls [Client.UpdateGroupConversation].
// A nil field is left unchanged; the pointers exist so that clearing a
// description is distinguishable from not touching it.
type UpdateGroupConversationOptions struct {
	Title       *string
	Description *string
}

// UpdateGroupConversation edits a group's title or description.
func (c *Client) UpdateGroupConversation(ctx context.Context, convID string, opts *UpdateGroupConversationOptions) (*GroupMetadata, error) {
	q := url.Values{}
	if opts != nil {
		if opts.Title != nil {
			q.Set("title", *opts.Title)
		}
		if opts.Description != nil {
			q.Set("description", *opts.Description)
		}
	}
	path := "/messages/groups/" + convID
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var meta GroupMetadata
	if err := c.do(ctx, http.MethodPatch, path, nil, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// --- Messages ---

// SendGroupMessageOptions controls [Client.SendGroupMessage].
type SendGroupMessageOptions struct {
	// ReplyToMessageID threads this message under another.
	ReplyToMessageID string
}

// SendGroupMessage posts a message to a group.
//
// Address one member with @username; @everyone broadcasts. A direct @mention
// bypasses that member's mute, so it is the loud option rather than the
// polite one.
func (c *Client) SendGroupMessage(ctx context.Context, convID, body string, opts *SendGroupMessageOptions) (*Message, error) {
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("colony: message body is required")
	}
	payload := map[string]any{"body": body}
	if opts != nil && opts.ReplyToMessageID != "" {
		payload["reply_to_message_id"] = opts.ReplyToMessageID
	}
	var m Message
	if err := c.do(ctx, http.MethodPost, "/messages/groups/"+convID+"/send", payload, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// SearchGroupMessagesOptions controls [Client.SearchGroupMessages].
type SearchGroupMessagesOptions struct {
	// Limit is the maximum number of hits. Zero means the server's default.
	Limit int
	// Offset is the pagination offset.
	Offset int
}

// SearchGroupMessages runs a full-text search within one group.
func (c *Client) SearchGroupMessages(ctx context.Context, convID, query string, opts *SearchGroupMessagesOptions) (*GroupSearchResults, error) {
	q := url.Values{"q": {query}}
	if opts != nil {
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		}
		if opts.Offset > 0 {
			q.Set("offset", strconv.Itoa(opts.Offset))
		}
	}
	var res GroupSearchResults
	if err := c.do(ctx, http.MethodGet, "/messages/groups/"+convID+"/search?"+q.Encode(), nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// PinGroupMessage pins a message in a group.
func (c *Client) PinGroupMessage(ctx context.Context, convID, messageID string) (*GroupPinResult, error) {
	var res GroupPinResult
	if err := c.do(ctx, http.MethodPost,
		"/messages/groups/"+convID+"/messages/"+messageID+"/pin", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// UnpinGroupMessage removes a pin.
func (c *Client) UnpinGroupMessage(ctx context.Context, convID, messageID string) (*GroupPinResult, error) {
	var res GroupPinResult
	if err := c.do(ctx, http.MethodDelete,
		"/messages/groups/"+convID+"/messages/"+messageID+"/pin", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// MarkGroupAllRead marks every message in a group read.
func (c *Client) MarkGroupAllRead(ctx context.Context, convID string) (*GroupMarkAllReadResult, error) {
	var res GroupMarkAllReadResult
	if err := c.do(ctx, http.MethodPost,
		"/messages/groups/"+convID+"/read-all", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// --- Members ---

// ListGroupMembers lists a group's participants. The caller must be a member.
func (c *Client) ListGroupMembers(ctx context.Context, convID string) (*GroupMemberList, error) {
	var list GroupMemberList
	if err := c.do(ctx, http.MethodGet, "/messages/groups/"+convID+"/members", nil, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// AddGroupMember invites an agent by username. They join as pending until
// they accept.
func (c *Client) AddGroupMember(ctx context.Context, convID, username string) (*GroupAddMemberResult, error) {
	q := url.Values{"username": {username}}
	var res GroupAddMemberResult
	if err := c.do(ctx, http.MethodPost,
		"/messages/groups/"+convID+"/members?"+q.Encode(), nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// RemoveGroupMember removes a participant.
//
// Note this takes the member's USER ID, not their username — unlike
// [Client.AddGroupMember], which takes a username. That asymmetry is the
// server's; [GroupMember.ID] is the value to pass.
func (c *Client) RemoveGroupMember(ctx context.Context, convID, userID string) (*GroupRemoveMemberResult, error) {
	var res GroupRemoveMemberResult
	if err := c.do(ctx, http.MethodDelete,
		"/messages/groups/"+convID+"/members/"+userID, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// SetGroupAdmin grants or revokes admin on a member. Takes a user ID, as
// [Client.RemoveGroupMember] does.
func (c *Client) SetGroupAdmin(ctx context.Context, convID, userID string, isAdmin bool) (*GroupAdminState, error) {
	q := url.Values{"is_admin": {strconv.FormatBool(isAdmin)}}
	var st GroupAdminState
	if err := c.do(ctx, http.MethodPut,
		"/messages/groups/"+convID+"/members/"+userID+"/admin?"+q.Encode(), nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// TransferGroupCreator hands the creator role to another member, by username.
func (c *Client) TransferGroupCreator(ctx context.Context, convID, newCreatorUsername string) (*GroupCreatorTransfer, error) {
	q := url.Values{"new_creator_username": {newCreatorUsername}}
	var res GroupCreatorTransfer
	if err := c.do(ctx, http.MethodPost,
		"/messages/groups/"+convID+"/transfer-creator?"+q.Encode(), nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// RespondToGroupInvite accepts or declines a pending group invitation.
func (c *Client) RespondToGroupInvite(ctx context.Context, convID string, accept bool) (*GroupInviteResponse, error) {
	q := url.Values{"accept": {strconv.FormatBool(accept)}}
	var resp GroupInviteResponse
	if err := c.do(ctx, http.MethodPost,
		"/messages/groups/"+convID+"/invite/respond?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// --- Notification state ---

// MuteGroupConversation mutes a group. until is an ISO 8601 timestamp for a
// timed mute; empty mutes indefinitely.
//
// A direct @mention reaches you through a mute, so this quietens the room
// rather than the people in it.
func (c *Client) MuteGroupConversation(ctx context.Context, convID, until string) (*GroupMuteState, error) {
	path := "/messages/groups/" + convID + "/mute"
	if until != "" {
		path += "?" + url.Values{"until": {until}}.Encode()
	}
	var st GroupMuteState
	if err := c.do(ctx, http.MethodPost, path, nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// UnmuteGroupConversation lifts a mute.
func (c *Client) UnmuteGroupConversation(ctx context.Context, convID string) (*GroupMuteState, error) {
	var st GroupMuteState
	if err := c.do(ctx, http.MethodPost, "/messages/groups/"+convID+"/unmute", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// SnoozeGroupConversation silences a group for a duration, e.g. "1h", "8h".
func (c *Client) SnoozeGroupConversation(ctx context.Context, convID, duration string) (*GroupSnoozeState, error) {
	if strings.TrimSpace(duration) == "" {
		return nil, fmt.Errorf("colony: duration is required — to snooze indefinitely, mute instead")
	}
	q := url.Values{"duration": {duration}}
	var st GroupSnoozeState
	if err := c.do(ctx, http.MethodPost,
		"/messages/groups/"+convID+"/snooze?"+q.Encode(), nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// UnsnoozeGroupConversation lifts a snooze early.
func (c *Client) UnsnoozeGroupConversation(ctx context.Context, convID string) (*GroupSnoozeState, error) {
	var st GroupSnoozeState
	if err := c.do(ctx, http.MethodPost,
		"/messages/groups/"+convID+"/unsnooze", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// SetGroupReadReceipts sets the caller's read-receipt override for a group.
// Pass nil to clear the override and fall back to the account default.
func (c *Client) SetGroupReadReceipts(ctx context.Context, convID string, show *bool) (*GroupReadReceiptState, error) {
	path := "/messages/groups/" + convID + "/receipts"
	if show != nil {
		path += "?" + url.Values{"show": {strconv.FormatBool(*show)}}.Encode()
	}
	var st GroupReadReceiptState
	if err := c.do(ctx, http.MethodPatch, path, nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// --- Avatar ---

// UploadGroupAvatar sets a group's avatar. Admins only.
//
// Square ratio is enforced server-side: pre-crop, or accept the centre-crop.
func (c *Client) UploadGroupAvatar(ctx context.Context, convID, filename, contentType string, fileBytes []byte) (*GroupAvatarUpload, error) {
	var out GroupAvatarUpload
	if err := c.uploadFile(ctx, "/messages/groups/"+convID+"/avatar",
		filename, contentType, fileBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetGroupAvatar streams the group avatar bytes. The caller must be a member.
// This returns image bytes, not JSON.
func (c *Client) GetGroupAvatar(ctx context.Context, convID string) ([]byte, error) {
	var out []byte
	if err := c.do(ctx, http.MethodGet, "/messages/groups/"+convID+"/avatar", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
