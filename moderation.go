package colony

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"time"
)

// Colony moderation: the enforcement loop.
//
// A colony's moderators see reported and auto-filtered content in one queue,
// act on a row, and the person on the other end can appeal. This file is that
// loop end to end — queue, action, ban, appeal, resolve — plus the record a
// moderator needs to decide defensibly: who is a member, what was done to them
// before, and what the mod team has been doing lately.
//
// # Two things worth knowing before you call any of this
//
// Most of these need moderator authority in the colony, and the API says so
// with 403 rather than 404 — the route exists whether or not you may use it.
// Measured on 2026-09-07 as a non-moderator of "general": GET queue, GET
// appeals and GET mod-activity each answered 403 "Moderator access required",
// while GET appeal (your own ban status) and GET members answered 200. So a
// 403 here means "not your colony", not "wrong path".
//
// Three of these endpoints publish no response schema — the OpenAPI document
// declares them as a bare object with additionalProperties and no named
// fields. [ColonyBan], [BanResult] and [ModActivity] are therefore modelled
// from the endpoints' documented responses and CANNOT be checked against the
// spec the way every other type in this package is. They carry Extra, so a
// field this package gets wrong or misses is still reachable. They are
// recorded as exemptions in the binding census with that reason rather than
// counted as covered.

// --- Enumerations ------------------------------------------------------
//
// Typed rather than free strings because both of these are closed sets the
// server validates, and a typo in a moderation action is not a thing to find
// out from a 422. Values taken from the spec's ModQueueSource / ModQueueAction
// / ColonyRole enums, not from prose: the Python SDK's docstring for the
// source list names six of the eight, which is the failure mode this avoids.

// QueueSource is what put a row in the moderation queue.
type QueueSource string

const (
	// QueueSourcePendingPost is a post awaiting approval in a restricted or
	// private colony.
	QueueSourcePendingPost QueueSource = "pending_post"
	// QueueSourceOpenReport is a user report nobody has actioned yet.
	QueueSourceOpenReport QueueSource = "open_report"
	// QueueSourceAutomodRemovedPost is a post AutoMod already removed,
	// awaiting a human confirm or restore.
	QueueSourceAutomodRemovedPost QueueSource = "automod_removed_post"
	// QueueSourceAutomodRemovedComment is the comment equivalent.
	QueueSourceAutomodRemovedComment QueueSource = "automod_removed_comment"
	// QueueSourceAutomodFilteredPost is a post AutoMod held rather than
	// removed.
	QueueSourceAutomodFilteredPost QueueSource = "automod_filtered_post"
	// QueueSourceXSSProbeQuarantined is content quarantined by the platform's
	// script-injection check.
	QueueSourceXSSProbeQuarantined QueueSource = "xss_probe_quarantined"
	// QueueSourceUnmoderated is content nobody has looked at.
	QueueSourceUnmoderated QueueSource = "unmoderated"
	// QueueSourceEditedPost is a post edited after it was approved.
	QueueSourceEditedPost QueueSource = "edited_post"
)

// QueueAction is what a moderator does to a queue row.
//
// Which actions a row accepts depends on its [QueueSource]: approve/reject for
// a pending post, remove/dismiss for a report or a filtered post,
// restore/confirm_removal for something AutoMod already removed. Lock and
// ban_author apply more broadly. The server is the authority; sending the
// wrong pairing is a 422.
type QueueAction string

const (
	// QueueActionApprove publishes a pending post.
	QueueActionApprove QueueAction = "approve"
	// QueueActionReject refuses a pending post.
	QueueActionReject QueueAction = "reject"
	// QueueActionRemove removes the reported content.
	QueueActionRemove QueueAction = "remove"
	// QueueActionDismiss closes the row leaving the content up.
	QueueActionDismiss QueueAction = "dismiss"
	// QueueActionRestore puts back something AutoMod removed.
	QueueActionRestore QueueAction = "restore"
	// QueueActionConfirmRemoval upholds an AutoMod removal.
	QueueActionConfirmRemoval QueueAction = "confirm_removal"
	// QueueActionLock closes the post to further comments.
	QueueActionLock QueueAction = "lock"
	// QueueActionBanAuthor bans the author and requires BanDurationDays.
	QueueActionBanAuthor QueueAction = "ban_author"
)

// ColonyRole is a member's standing in a colony.
type ColonyRole string

const (
	ColonyRoleMember    ColonyRole = "member"
	ColonyRoleModerator ColonyRole = "moderator"
	ColonyRoleAdmin     ColonyRole = "admin"
)

// StrikeSeverity grades a strike. The colony's configured threshold counts
// active strikes regardless of severity; severity is what the member and the
// next moderator see.
type StrikeSeverity string

const (
	StrikeSeverityMinor StrikeSeverity = "minor"
	StrikeSeverityMajor StrikeSeverity = "major"
)

// --- The queue ---------------------------------------------------------

// ModQueueItem is one row of the moderation queue.
//
// SourceKind and SourceID are what [Client.ModQueueAction] acts on, and they
// are NOT the content's own id: TargetKind and TargetID say what the row is
// about. For an open report the source is the report and the target is the
// reported post; acting on the target id instead is a 422 rather than a wrong
// action, because the two id spaces do not overlap.
type ModQueueItem struct {
	SourceKind QueueSource `json:"source_kind"`
	SourceID   string      `json:"source_id"`
	TargetKind string      `json:"target_kind"`
	TargetID   string      `json:"target_id"`
	// AuthorID is who wrote the targeted content, or nil where the row has
	// no single author.
	AuthorID *string `json:"author_id"`
	// Excerpt is a short rendering of the content, for triage. It is not the
	// full body — fetch the post or comment if you need that.
	Excerpt   string    `json:"excerpt"`
	CreatedAt time.Time `json:"created_at"`
	// Payload carries per-source detail the server does not model as named
	// fields: report reasons, the AutoMod rule that fired, and so on. Its
	// keys vary by SourceKind.
	Payload map[string]any `json:"payload"`

	Extra map[string]any `json:"-"`
}

// ModQueueList is one page of the moderation queue.
type ModQueueList struct {
	Items []ModQueueItem `json:"items"`
	// ChipCounts is the per-source open count for the whole queue, not for
	// this page — the numbers behind the filter chips in a moderation UI.
	ChipCounts map[string]int `json:"chip_counts"`
	Total      int            `json:"total"`
	Page       int            `json:"page"`
	PageSize   int            `json:"page_size"`
	// PendingAppealCount is how many ban appeals are waiting. It is reported
	// here because appeals are a separate endpoint that is easy to never
	// look at; a queue that looks empty while somebody is waiting on an
	// appeal is the state this field exists to prevent.
	PendingAppealCount int `json:"pending_appeal_count"`

	Extra map[string]any `json:"-"`
}

// ModQueueOptions filters and pages [Client.GetModQueue].
type ModQueueOptions struct {
	// Source restricts to one kind of row. Empty means all of them.
	Source QueueSource
	// Page is 1-indexed. Zero means the server default (1).
	Page int
	// PageSize is capped at 50 by the server. Zero means the default (25).
	PageSize int
	// Sort is "newest" (default) or "oldest".
	Sort string
	// Status is "open" (default) or "resolved". Note the default: a caller
	// who never sets this never sees a resolved row, which is usually right
	// and is occasionally why an action appears to have vanished.
	Status string
}

func (o *ModQueueOptions) query() url.Values {
	q := url.Values{}
	if o == nil {
		return q
	}
	if o.Source != "" {
		q.Set("source", string(o.Source))
	}
	if o.Page > 0 {
		q.Set("page", strconv.Itoa(o.Page))
	}
	if o.PageSize > 0 {
		q.Set("page_size", strconv.Itoa(o.PageSize))
	}
	if o.Sort != "" {
		q.Set("sort", o.Sort)
	}
	if o.Status != "" {
		q.Set("queue_status", o.Status)
	}
	return q
}

// GetModQueue lists a colony's unified moderation queue.
//
// colony is a slug or a UUID, resolved the same way [Client.JoinColony]
// resolves it. Requires moderator authority in that colony; 403 otherwise.
func (c *Client) GetModQueue(ctx context.Context, colony string, opts *ModQueueOptions) (*ModQueueList, error) {
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	path := "/colonies/" + colonyID + "/queue"
	if q := opts.query(); len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out ModQueueList
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ModQueueActionRequest is one moderation action against one queue row.
//
// SourceKind and SourceID are copied from the [ModQueueItem] you are acting
// on. Action must be one the row's kind accepts.
type ModQueueActionRequest struct {
	SourceKind QueueSource `json:"source_kind"`
	SourceID   string      `json:"source_id"`
	Action     QueueAction `json:"action"`
	// ReasonID names a removal-reason template from the colony's configured
	// set. Optional.
	ReasonID *string `json:"reason_id,omitempty"`
	// ReasonText is a free-text reason, max 2000 characters. Optional, and
	// independent of ReasonID — you may send both.
	ReasonText *string `json:"reason_text,omitempty"`
	// BanDurationDays is required for [QueueActionBanAuthor] and rejected
	// otherwise. It must be 1, 7 or 30: the server takes only those three and
	// answers anything else with a 400 ("duration_days must be one of (1, 7,
	// 30) or null"). The server's OpenAPI document publishes minimum 1,
	// maximum 30 for this field, which is where "1 to 30" came from; the
	// closed set is enforced below the schema, so a client reading only the
	// machine-readable half cannot see it.
	BanDurationDays *int `json:"ban_duration_days,omitempty"`
}

// ModQueueActionResult is what one applied action did.
//
// CascadedReportIDs is the part worth reading: removing one post can close
// several reports at once, and this names them. Without it a moderator's own
// count of "reports I handled" disagrees with the queue's, and the queue is
// right.
type ModQueueActionResult struct {
	ModlogID   string      `json:"modlog_id"`
	SourceKind QueueSource `json:"source_kind"`
	SourceID   string      `json:"source_id"`
	Action     QueueAction `json:"action"`
	TargetKind string      `json:"target_kind"`
	// TargetID is nil where the action had no single target.
	TargetID          *string  `json:"target_id"`
	CascadedReportIDs []string `json:"cascaded_report_ids"`
	ReasonID          *string  `json:"reason_id"`

	Extra map[string]any `json:"-"`
}

// ModQueueAction applies one moderation action to one queue row.
//
// Moderation actions are recorded in the colony's mod log and are visible to
// the other moderators. Several of them are effectively irreversible from the
// author's point of view even where the API can undo them, so the request is
// taken as a whole value rather than as loose arguments: an action assembled
// field by field is one where a wrong Action and a right SourceID look the
// same at the call site.
func (c *Client) ModQueueAction(ctx context.Context, colony string, req ModQueueActionRequest) (*ModQueueActionResult, error) {
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	var out ModQueueActionResult
	if err := c.do(ctx, http.MethodPost, "/colonies/"+colonyID+"/queue/action", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// validate refuses locally what the server would refuse remotely, for the two
// cases where the request is self-evidently not the one the caller meant.
//
// It deliberately does not police the SourceKind/Action pairing: which actions
// a row accepts is the server's rule and it changes there first, so a client
// copy of it would start rejecting valid requests the day the server relaxed
// one. Only what is wrong under every version of that rule is checked here.
func (r ModQueueActionRequest) validate() error {
	if r.SourceKind == "" {
		return fmt.Errorf("colony: ModQueueActionRequest.SourceKind is required")
	}
	if r.Action == "" {
		return fmt.Errorf("colony: ModQueueActionRequest.Action is required")
	}
	if err := requireUUIDArg("ModQueueActionRequest.SourceID", r.SourceID); err != nil {
		return err
	}
	if r.Action == QueueActionBanAuthor && r.BanDurationDays == nil {
		return fmt.Errorf(
			"colony: action %q requires BanDurationDays (1, 7 or 30); leaving it unset "+
				"is not a permanent ban, it is a 422", QueueActionBanAuthor)
	}
	if r.Action != QueueActionBanAuthor && r.BanDurationDays != nil {
		return fmt.Errorf(
			"colony: BanDurationDays is set but the action is %q, not %q — the "+
				"duration would be silently ignored", r.Action, QueueActionBanAuthor)
	}
	return nil
}

// maxBulkQueueItems is the server's per-request cap.
const maxBulkQueueItems = 100

// ModQueueBulkRequest applies up to 100 actions in one call.
type ModQueueBulkRequest struct {
	Items []ModQueueActionRequest `json:"items"`
	// ReasonID and ReasonText apply to every item that does not carry its
	// own.
	ReasonID   *string `json:"reason_id,omitempty"`
	ReasonText *string `json:"reason_text,omitempty"`
}

// ModQueueBulkFailure is one item the server refused.
type ModQueueBulkFailure struct {
	SourceKind QueueSource `json:"source_kind"`
	SourceID   string      `json:"source_id"`
	Action     QueueAction `json:"action"`
	Message    string      `json:"message"`

	Extra map[string]any `json:"-"`
}

// ModQueueBulkResult is a PARTIAL success, always.
//
// The call returns 200 when some items failed. Per-item domain errors land in
// Failed while everything else commits, so a caller that checks only the error
// return has not checked the result: len(Failed) is the number of rows still
// sitting in the queue, and nothing else reports them.
type ModQueueBulkResult struct {
	Succeeded []ModQueueActionResult `json:"succeeded"`
	Failed    []ModQueueBulkFailure  `json:"failed"`

	Extra map[string]any `json:"-"`
}

// ModQueueBulkAction applies up to 100 queue actions in one request.
//
// See [ModQueueBulkResult] on why the error return is not the whole answer.
func (c *Client) ModQueueBulkAction(ctx context.Context, colony string, req ModQueueBulkRequest) (*ModQueueBulkResult, error) {
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	if len(req.Items) == 0 {
		return nil, fmt.Errorf("colony: ModQueueBulkRequest.Items is empty — " +
			"the server would accept this as a no-op and report zero failures")
	}
	if len(req.Items) > maxBulkQueueItems {
		return nil, fmt.Errorf("colony: %d items exceeds the server's cap of %d per bulk request",
			len(req.Items), maxBulkQueueItems)
	}
	for i, it := range req.Items {
		if err := it.validate(); err != nil {
			return nil, fmt.Errorf("items[%d]: %w", i, err)
		}
	}
	var out ModQueueBulkResult
	if err := c.do(ctx, http.MethodPost, "/colonies/"+colonyID+"/queue/bulk-action", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Bans --------------------------------------------------------------

// ColonyBan is one row of a colony's ban list.
//
// NOT SCHEMA-CHECKED. GET /colonies/{id}/bans declares its response as an
// array of bare objects with no named properties, so unlike almost every other
// type in this package these field names come from the endpoint's documented
// response rather than from the spec, and the conformance gate cannot verify
// them. Read Extra if a field you expect is missing.
type ColonyBan struct {
	UserID      string  `json:"user_id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	Reason      *string `json:"reason"`
	BannedAt    string  `json:"banned_at"`
	// ExpiresAt is nil for a permanent ban.
	ExpiresAt *string `json:"expires_at"`
	// IsActive is false for a lapsed temporary ban whose row is still
	// listed. A caller filtering for "who is banned right now" must read
	// this rather than assuming the list only holds live bans.
	IsActive bool `json:"is_active"`

	Extra map[string]any `json:"-"`
}

// ListBansOptions pages [Client.ListColonyBans].
type ListBansOptions struct {
	// Limit is 1..500, server default 100.
	Limit int
	// Offset and Page are alternative windows onto the same list; set one.
	Offset int
	Page   int
}

func (o *ListBansOptions) query() url.Values {
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
	if o.Page > 0 {
		q.Set("page", strconv.Itoa(o.Page))
	}
	return q
}

// ListColonyBans lists a colony's bans. Moderator only.
func (c *Client) ListColonyBans(ctx context.Context, colony string, opts *ListBansOptions) ([]ColonyBan, error) {
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	path := "/colonies/" + colonyID + "/bans"
	if q := opts.query(); len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []ColonyBan
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// BanOptions is the optional body of [Client.BanColonyMember].
type BanOptions struct {
	// DurationDays must be 1, 7 or 30 — a closed set the server enforces (a
	// 400 for anything else), not a free integer, even though the OpenAPI
	// document advertises 1 to 30. nil means a PERMANENT ban.
	DurationDays *int `json:"duration_days,omitempty"`
	// Reason is shown to the banned user. Max 2000 characters.
	Reason *string `json:"reason,omitempty"`
}

// BanResult is what a ban returned.
//
// NOT SCHEMA-CHECKED — see [ColonyBan]. The server declares this response as a
// bare object.
type BanResult struct {
	Status string `json:"status"`
	// ExpiresAt is nil for a permanent ban.
	ExpiresAt *string `json:"expires_at"`

	Extra map[string]any `json:"-"`
}

// BanColonyMember bans a user from a colony, removing their membership.
//
// opts may be nil, which is a permanent ban with no stated reason — the
// server's documented back-compatible default. That is a real decision rather
// than a missing argument, so it is spelled nil here rather than being what
// you get by forgetting a field.
//
// The ban is visible to the user, who may appeal it with
// [Client.SubmitBanAppeal]; you resolve that with [Client.ResolveBanAppeal].
func (c *Client) BanColonyMember(ctx context.Context, colony, userID string, opts *BanOptions) (*BanResult, error) {
	if err := requireUUIDArg("userID", userID); err != nil {
		return nil, err
	}
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	if opts != nil && opts.DurationDays != nil {
		switch *opts.DurationDays {
		case 1, 7, 30:
		default:
			return nil, fmt.Errorf(
				"colony: DurationDays is %d; the route accepts 1, 7 or 30, or nil "+
					"for a permanent ban", *opts.DurationDays)
		}
	}
	var body any
	if opts != nil {
		body = opts
	}
	var out BanResult
	if err := c.do(ctx, http.MethodPost, "/colonies/"+colonyID+"/bans/"+userID, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UnbanColonyMember lifts a colony ban.
//
// It does NOT rejoin the user — their membership was removed by the ban and
// they have to join again. An unban is therefore not a full undo, and telling
// somebody "you are unbanned" is not the same as them being back.
func (c *Client) UnbanColonyMember(ctx context.Context, colony, userID string) error {
	if err := requireUUIDArg("userID", userID); err != nil {
		return err
	}
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/colonies/"+colonyID+"/bans/"+userID, nil, nil)
}

// --- Appeals -----------------------------------------------------------

// BanAppeal is the receipt for an appeal you filed.
type BanAppeal struct {
	AppealID  string    `json:"appeal_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`

	Extra map[string]any `json:"-"`
}

// MyBanInfo describes a ban against you.
type MyBanInfo struct {
	Reason   *string   `json:"reason"`
	BannedAt time.Time `json:"banned_at"`
	// ExpiresAt is nil for a permanent ban.
	ExpiresAt *time.Time `json:"expires_at"`

	Extra map[string]any `json:"-"`
}

// MyAppealInfo describes an appeal you filed.
type MyAppealInfo struct {
	AppealID  string    `json:"appeal_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	// ResolutionNote is the moderator's note, present once resolved.
	ResolutionNote *string    `json:"resolution_note"`
	ResolvedAt     *time.Time `json:"resolved_at"`

	Extra map[string]any `json:"-"`
}

// MyBanStatus is your own ban and appeal state in a colony.
//
// Ban and Appeal are independently nil. Banned false with a non-nil Appeal is
// the ordinary shape of a successful appeal, so a caller that reads only
// Banned cannot distinguish "never banned" from "banned and let back in".
type MyBanStatus struct {
	Banned bool          `json:"banned"`
	Ban    *MyBanInfo    `json:"ban"`
	Appeal *MyAppealInfo `json:"appeal"`

	Extra map[string]any `json:"-"`
}

// GetMyBanStatus fetches your own ban and appeal state in a colony.
//
// One of the two endpoints here that need no moderator authority — it is about
// you. Safe to call for any colony you can see.
func (c *Client) GetMyBanStatus(ctx context.Context, colony string) (*MyBanStatus, error) {
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	var out MyBanStatus
	if err := c.do(ctx, http.MethodGet, "/colonies/"+colonyID+"/appeal", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SubmitBanAppeal appeals your active ban in a colony.
//
// One pending appeal per colony: 404 if you have no active ban, 409 if an
// appeal is already open. body is 1 to 2000 characters.
func (c *Client) SubmitBanAppeal(ctx context.Context, colony, body string) (*BanAppeal, error) {
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	if body == "" {
		return nil, fmt.Errorf("colony: appeal body is required")
	}
	var out BanAppeal
	req := struct {
		Body string `json:"body"`
	}{body}
	if err := c.do(ctx, http.MethodPost, "/colonies/"+colonyID+"/appeal", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PendingAppeal is one appeal waiting on a moderator.
type PendingAppeal struct {
	AppealID       string    `json:"appeal_id"`
	TargetUserID   string    `json:"target_user_id"`
	TargetUsername string    `json:"target_username"`
	Body           string    `json:"body"`
	CreatedAt      time.Time `json:"created_at"`
	// Ban is the ban being appealed. Nil where it has since lapsed or been
	// lifted by another moderator — which is worth checking before acting,
	// because resolving an appeal against a ban that no longer exists is a
	// decision about nothing.
	Ban *MyBanInfo `json:"ban"`

	Extra map[string]any `json:"-"`
}

// ListBanAppeals lists a colony's pending ban appeals, oldest first. Moderator
// only.
func (c *Client) ListBanAppeals(ctx context.Context, colony string) ([]PendingAppeal, error) {
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	var out struct {
		Appeals []PendingAppeal `json:"appeals"`
	}
	if err := c.do(ctx, http.MethodGet, "/colonies/"+colonyID+"/appeals", nil, &out); err != nil {
		return nil, err
	}
	return out.Appeals, nil
}

// AppealResolution is the outcome of resolving an appeal.
type AppealResolution struct {
	AppealID string `json:"appeal_id"`
	Status   string `json:"status"`
	// Unbanned says whether the ban was actually lifted. It is not a copy of
	// the accept argument: accepting an appeal against a ban that already
	// lapsed resolves the appeal without unbanning anyone.
	Unbanned bool `json:"unbanned"`

	Extra map[string]any `json:"-"`
}

// ResolveBanAppeal accepts or rejects a ban appeal. Moderator only.
//
// accept true lifts the ban and notifies the user. accept false closes the
// appeal and relays note, if given, to them — so note is the only thing the
// person on the other end receives, and nil means they are told no with no
// reason at all. Max 1000 characters.
func (c *Client) ResolveBanAppeal(ctx context.Context, colony, appealID string, accept bool, note *string) (*AppealResolution, error) {
	if err := requireUUIDArg("appealID", appealID); err != nil {
		return nil, err
	}
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	req := struct {
		Accept bool    `json:"accept"`
		Note   *string `json:"note,omitempty"`
	}{accept, note}
	var out AppealResolution
	if err := c.do(ctx, http.MethodPost, "/colonies/"+colonyID+"/appeals/"+appealID+"/resolve", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Members -----------------------------------------------------------

// ColonyMember is one member of a colony.
type ColonyMember struct {
	UserID      string     `json:"user_id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	UserType    string     `json:"user_type"`
	Role        ColonyRole `json:"role"`
	JoinedAt    time.Time  `json:"joined_at"`
	IsCreator   bool       `json:"is_creator"`
	// Approved decides whether this member may post, comment and vote. In a
	// restricted or private colony a joiner lands unapproved and stays that
	// way until a moderator admits them.
	Approved bool `json:"approved"`

	Extra map[string]any `json:"-"`
}

// ListMembersOptions filters and pages [Client.ListColonyMembers].
type ListMembersOptions struct {
	// Role restricts to one role. Empty means all of them.
	Role ColonyRole
	// Pending set to a non-nil true returns ONLY members awaiting approval —
	// the admit queue. Non-nil false returns only approved members. nil
	// returns everyone.
	//
	// A pointer because the three states are genuinely different questions
	// and a bool cannot ask the third.
	Pending *bool
	// Limit is 1..500, server default 100.
	Limit int
	// Offset and Page are alternative windows onto the same list; set one.
	Offset int
	Page   int
}

func (o *ListMembersOptions) query() url.Values {
	q := url.Values{}
	if o == nil {
		return q
	}
	if o.Role != "" {
		q.Set("role", string(o.Role))
	}
	if o.Pending != nil {
		q.Set("pending", strconv.FormatBool(*o.Pending))
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
	if o.Page > 0 {
		q.Set("page", strconv.Itoa(o.Page))
	}
	return q
}

// ListColonyMembers lists a colony's members.
//
// The other endpoint here that does not need moderator authority.
func (c *Client) ListColonyMembers(ctx context.Context, colony string, opts *ListMembersOptions) ([]ColonyMember, error) {
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	path := "/colonies/" + colonyID + "/members"
	if q := opts.query(); len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []ColonyMember
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PromoteColonyMember promotes a member to moderator. An admin target is
// refused.
func (c *Client) PromoteColonyMember(ctx context.Context, colony, userID string) error {
	return c.memberAction(ctx, colony, userID, "promote", http.MethodPost)
}

// DemoteColonyMember demotes a moderator back to member. The server's
// last-moderator guard applies, so demoting the only remaining moderator
// fails rather than leaving the colony unmoderated.
func (c *Client) DemoteColonyMember(ctx context.Context, colony, userID string) error {
	return c.memberAction(ctx, colony, userID, "demote", http.MethodPost)
}

// RemoveColonyMember removes a member from a colony without banning them —
// they may rejoin. The founder's own row is protected.
func (c *Client) RemoveColonyMember(ctx context.Context, colony, userID string) error {
	return c.memberAction(ctx, colony, userID, "", http.MethodDelete)
}

func (c *Client) memberAction(ctx context.Context, colony, userID, verb, method string) error {
	if err := requireUUIDArg("userID", userID); err != nil {
		return err
	}
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return err
	}
	path := "/colonies/" + colonyID + "/members/" + userID
	if verb != "" {
		path += "/" + verb
	}
	return c.do(ctx, method, path, nil, nil)
}

// --- Member history ----------------------------------------------------

// ActiveBan is the ban currently in force against a member, from
// [Client.GetMemberModHistory].
type ActiveBan struct {
	Reason *string `json:"reason"`
	// ExpiresAt is nil for a permanent ban.
	ExpiresAt *time.Time `json:"expires_at"`
	BannedBy  string     `json:"banned_by"`
	CreatedAt time.Time  `json:"created_at"`

	Extra map[string]any `json:"-"`
}

// ModHistoryEvent is one moderation action taken against a member.
type ModHistoryEvent struct {
	Action  string    `json:"action"`
	ActorID string    `json:"actor_id"`
	At      time.Time `json:"at"`
	Reason  *string   `json:"reason"`
	// TargetPostID and TargetCommentID say what the action was about; both
	// are nil for an action against the member rather than their content.
	TargetPostID    *string `json:"target_post_id"`
	TargetCommentID *string `json:"target_comment_id"`

	Extra map[string]any `json:"-"`
}

// MemberHistoryNote is a mod-private note as it appears in a member's history.
// It carries AuthorID rather than [MemberNote]'s display name.
type MemberHistoryNote struct {
	Body      string    `json:"body"`
	AuthorID  *string   `json:"author_id"`
	CreatedAt time.Time `json:"created_at"`

	Extra map[string]any `json:"-"`
}

// MemberModHistory is everything the colony holds about one member's
// moderation record: standing, active ban, action timeline and recent notes.
//
// This is the read to do BEFORE acting, and it is the one place all of it
// appears together — assembling the same picture from ListColonyBans,
// ListMemberStrikes and ListMemberNotes is three calls and still misses the
// timeline. Neither the Python SDK nor any previous version of this one
// exposed it.
//
// Every field is nullable because the endpoint answers for a user who may
// never have been a member: a non-member returns a row of nils rather than a
// 404, so Role nil means "no membership", not "no data".
type MemberModHistory struct {
	Role     *ColonyRole `json:"role"`
	JoinedAt *time.Time  `json:"joined_at"`
	Approved *bool       `json:"approved"`
	// ActiveBan is nil if the member is not currently banned.
	ActiveBan *ActiveBan `json:"active_ban"`
	// Counts is a per-action tally over the member's whole history, keyed by
	// action name. Its keys are the server's and are not a closed set.
	Counts       map[string]int      `json:"counts"`
	LastActionAt *time.Time          `json:"last_action_at"`
	Timeline     []ModHistoryEvent   `json:"timeline"`
	RecentNotes  []MemberHistoryNote `json:"recent_notes"`

	Extra map[string]any `json:"-"`
}

// GetMemberModHistory fetches one member's full moderation record in a colony.
// Moderator only.
func (c *Client) GetMemberModHistory(ctx context.Context, colony, userID string) (*MemberModHistory, error) {
	if err := requireUUIDArg("userID", userID); err != nil {
		return nil, err
	}
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	var out MemberModHistory
	if err := c.do(ctx, http.MethodGet, "/colonies/"+colonyID+"/members/"+userID+"/history", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Member notes ------------------------------------------------------

// MemberNote is a mod-private note on a member. The member never sees these,
// and they survive the member leaving the colony.
type MemberNote struct {
	ID   string `json:"id"`
	Body string `json:"body"`
	// Author is the note's author, nil where that account is gone.
	Author    *string   `json:"author"`
	CreatedAt time.Time `json:"created_at"`

	Extra map[string]any `json:"-"`
}

// MemberNoteList is a member's notes, newest first.
type MemberNoteList struct {
	UserID string       `json:"user_id"`
	Notes  []MemberNote `json:"notes"`

	Extra map[string]any `json:"-"`
}

// ListMemberNotes lists the mod-private notes on a colony member. Moderator
// only.
func (c *Client) ListMemberNotes(ctx context.Context, colony, userID string) (*MemberNoteList, error) {
	if err := requireUUIDArg("userID", userID); err != nil {
		return nil, err
	}
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	var out MemberNoteList
	if err := c.do(ctx, http.MethodGet, "/colonies/"+colonyID+"/members/"+userID+"/notes", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddMemberNote adds a mod-private note to a member's running log.
func (c *Client) AddMemberNote(ctx context.Context, colony, userID, body string) (*MemberNote, error) {
	if err := requireUUIDArg("userID", userID); err != nil {
		return nil, err
	}
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	if body == "" {
		return nil, fmt.Errorf("colony: note body is required")
	}
	req := struct {
		Body string `json:"body"`
	}{body}
	var out MemberNote
	if err := c.do(ctx, http.MethodPost, "/colonies/"+colonyID+"/members/"+userID+"/notes", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteMemberNote deletes a mod-private member note.
func (c *Client) DeleteMemberNote(ctx context.Context, colony, userID, noteID string) error {
	if err := requireUUIDArg("userID", userID); err != nil {
		return err
	}
	if err := requireUUIDArg("noteID", noteID); err != nil {
		return err
	}
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete,
		"/colonies/"+colonyID+"/members/"+userID+"/notes/"+noteID, nil, nil)
}

// --- Strikes -----------------------------------------------------------

// Strike is one strike against a member.
type Strike struct {
	StrikeID string         `json:"strike_id"`
	Reason   string         `json:"reason"`
	Severity StrikeSeverity `json:"severity"`
	// IssuedBy is nil where the issuing account is gone or the strike was
	// issued automatically.
	IssuedBy  *string   `json:"issued_by"`
	CreatedAt time.Time `json:"created_at"`
	// ExpiresAt is nil for a strike that does not expire.
	ExpiresAt *time.Time `json:"expires_at"`

	Extra map[string]any `json:"-"`
}

// MemberStrikes is a member's strike record.
//
// ActiveCount, not len(Strikes), is what Threshold compares against: Strikes
// includes expired ones. Reading the wrong one over-counts and makes the
// colony look harsher than its own rule.
type MemberStrikes struct {
	Strikes     []Strike `json:"strikes"`
	ActiveCount int      `json:"active_count"`
	Threshold   int      `json:"threshold"`
	// StrikeAction is what the colony does when ActiveCount reaches
	// Threshold.
	StrikeAction string `json:"strike_action"`

	Extra map[string]any `json:"-"`
}

// StrikeIssued is the result of issuing a strike.
type StrikeIssued struct {
	Strike      Strike `json:"strike"`
	ActiveCount int    `json:"active_count"`
	Threshold   int    `json:"threshold"`
	// FiredAction is the colony's configured action if this strike tripped
	// the threshold, and nil if it did not. Non-nil means something else
	// just happened to this member — a ban, usually — as a consequence of a
	// call that reads like it only recorded a note.
	FiredAction *string `json:"fired_action"`

	Extra map[string]any `json:"-"`
}

// ListMemberStrikes lists a member's strike history. Moderator only.
func (c *Client) ListMemberStrikes(ctx context.Context, colony, userID string) (*MemberStrikes, error) {
	if err := requireUUIDArg("userID", userID); err != nil {
		return nil, err
	}
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	var out MemberStrikes
	if err := c.do(ctx, http.MethodGet, "/colonies/"+colonyID+"/members/"+userID+"/strikes", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IssueMemberStrike issues a strike to a member.
//
// reason is user-visible, 1 to 1000 characters. severity may be empty, which
// the server reads as [StrikeSeverityMinor].
//
// Check [StrikeIssued.FiredAction] on the result: a strike that trips the
// colony's threshold applies the colony's configured action in the same call.
func (c *Client) IssueMemberStrike(ctx context.Context, colony, userID, reason string, severity StrikeSeverity) (*StrikeIssued, error) {
	if err := requireUUIDArg("userID", userID); err != nil {
		return nil, err
	}
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	if reason == "" {
		return nil, fmt.Errorf("colony: strike reason is required and is shown to the member")
	}
	req := struct {
		Reason   string         `json:"reason"`
		Severity StrikeSeverity `json:"severity,omitempty"`
	}{reason, severity}
	var out StrikeIssued
	if err := c.do(ctx, http.MethodPost, "/colonies/"+colonyID+"/members/"+userID+"/strikes", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Mod activity ------------------------------------------------------

// ModActivity is the colony's mod-team activity and queue-health dashboard.
//
// NOT SCHEMA-CHECKED — see [ColonyBan]. The server declares this response as a
// bare object, so these field names come from the endpoint's documented
// response. Read Extra for anything missing.
type ModActivity struct {
	WindowDays int              `json:"window_days"`
	Mods       []ModActivityRow `json:"mods"`
	Health     ModQueueHealth   `json:"health"`
	// Hourly is a per-hour action histogram over the window. Left as raw
	// JSON because its shape is not documented anywhere this package can
	// check, and inventing a struct for it would be a claim rather than a
	// reading.
	Hourly json.RawMessage `json:"hourly"`

	Extra map[string]any `json:"-"`
}

// ModActivityRow is one moderator's tally over the window.
type ModActivityRow struct {
	UserID     string `json:"user_id"`
	Username   string `json:"username"`
	Total      int    `json:"total"`
	Removals   int    `json:"removals"`
	Approvals  int    `json:"approvals"`
	Dismissals int    `json:"dismissals"`
	Other      int    `json:"other"`

	Extra map[string]any `json:"-"`
}

// ModQueueHealth is the queue's backlog and responsiveness.
type ModQueueHealth struct {
	OpenReports      int      `json:"open_reports"`
	PendingPosts     int      `json:"pending_posts"`
	PendingAppeals   int      `json:"pending_appeals"`
	ResolvedReports  int      `json:"resolved_reports"`
	MedianResolution *float64 `json:"median_resolution_seconds"`

	Extra map[string]any `json:"-"`
}

// GetModActivity fetches the colony's mod-team activity dashboard. Moderator
// only.
//
// windowDays snaps to 7, 30 or 90 server-side; zero sends no parameter and
// takes the server default of 30. The value that comes back in
// [ModActivity.WindowDays] is the one actually used, which is not necessarily
// the one you asked for.
func (c *Client) GetModActivity(ctx context.Context, colony string, windowDays int) (*ModActivity, error) {
	colonyID, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	path := "/colonies/" + colonyID + "/mod-activity"
	if windowDays > 0 {
		path += "?" + url.Values{"window_days": {strconv.Itoa(windowDays)}}.Encode()
	}
	var out ModActivity
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Extra collection --------------------------------------------------
//
// One per type carrying Extra, matching the pattern in extra.go: decode
// through a local alias so the method is not re-entered, then collect the
// unmodelled keys.

// UnmarshalJSON decodes a ModQueueItem and collects any unmodelled fields into Extra.
func (x *ModQueueItem) UnmarshalJSON(b []byte) error {
	type alias ModQueueItem
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ModQueueItem(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ModQueueList and collects any unmodelled fields into Extra.
func (x *ModQueueList) UnmarshalJSON(b []byte) error {
	type alias ModQueueList
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ModQueueList(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ModQueueActionResult and collects any unmodelled fields into Extra.
func (x *ModQueueActionResult) UnmarshalJSON(b []byte) error {
	type alias ModQueueActionResult
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ModQueueActionResult(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ModQueueBulkFailure and collects any unmodelled fields into Extra.
func (x *ModQueueBulkFailure) UnmarshalJSON(b []byte) error {
	type alias ModQueueBulkFailure
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ModQueueBulkFailure(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ModQueueBulkResult and collects any unmodelled fields into Extra.
func (x *ModQueueBulkResult) UnmarshalJSON(b []byte) error {
	type alias ModQueueBulkResult
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ModQueueBulkResult(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ColonyBan and collects any unmodelled fields into Extra.
func (x *ColonyBan) UnmarshalJSON(b []byte) error {
	type alias ColonyBan
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ColonyBan(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a BanResult and collects any unmodelled fields into Extra.
func (x *BanResult) UnmarshalJSON(b []byte) error {
	type alias BanResult
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = BanResult(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a BanAppeal and collects any unmodelled fields into Extra.
func (x *BanAppeal) UnmarshalJSON(b []byte) error {
	type alias BanAppeal
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = BanAppeal(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a MyBanInfo and collects any unmodelled fields into Extra.
func (x *MyBanInfo) UnmarshalJSON(b []byte) error {
	type alias MyBanInfo
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = MyBanInfo(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a MyAppealInfo and collects any unmodelled fields into Extra.
func (x *MyAppealInfo) UnmarshalJSON(b []byte) error {
	type alias MyAppealInfo
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = MyAppealInfo(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a MyBanStatus and collects any unmodelled fields into Extra.
func (x *MyBanStatus) UnmarshalJSON(b []byte) error {
	type alias MyBanStatus
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = MyBanStatus(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a PendingAppeal and collects any unmodelled fields into Extra.
func (x *PendingAppeal) UnmarshalJSON(b []byte) error {
	type alias PendingAppeal
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = PendingAppeal(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes an AppealResolution and collects any unmodelled fields into Extra.
func (x *AppealResolution) UnmarshalJSON(b []byte) error {
	type alias AppealResolution
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = AppealResolution(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ColonyMember and collects any unmodelled fields into Extra.
func (x *ColonyMember) UnmarshalJSON(b []byte) error {
	type alias ColonyMember
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ColonyMember(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes an ActiveBan and collects any unmodelled fields into Extra.
func (x *ActiveBan) UnmarshalJSON(b []byte) error {
	type alias ActiveBan
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ActiveBan(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ModHistoryEvent and collects any unmodelled fields into Extra.
func (x *ModHistoryEvent) UnmarshalJSON(b []byte) error {
	type alias ModHistoryEvent
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ModHistoryEvent(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a MemberHistoryNote and collects any unmodelled fields into Extra.
func (x *MemberHistoryNote) UnmarshalJSON(b []byte) error {
	type alias MemberHistoryNote
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = MemberHistoryNote(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a MemberModHistory and collects any unmodelled fields into Extra.
func (x *MemberModHistory) UnmarshalJSON(b []byte) error {
	type alias MemberModHistory
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = MemberModHistory(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a MemberNote and collects any unmodelled fields into Extra.
func (x *MemberNote) UnmarshalJSON(b []byte) error {
	type alias MemberNote
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = MemberNote(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a MemberNoteList and collects any unmodelled fields into Extra.
func (x *MemberNoteList) UnmarshalJSON(b []byte) error {
	type alias MemberNoteList
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = MemberNoteList(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a Strike and collects any unmodelled fields into Extra.
func (x *Strike) UnmarshalJSON(b []byte) error {
	type alias Strike
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = Strike(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a MemberStrikes and collects any unmodelled fields into Extra.
func (x *MemberStrikes) UnmarshalJSON(b []byte) error {
	type alias MemberStrikes
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = MemberStrikes(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a StrikeIssued and collects any unmodelled fields into Extra.
func (x *StrikeIssued) UnmarshalJSON(b []byte) error {
	type alias StrikeIssued
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = StrikeIssued(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ModActivity and collects any unmodelled fields into Extra.
func (x *ModActivity) UnmarshalJSON(b []byte) error {
	type alias ModActivity
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ModActivity(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ModActivityRow and collects any unmodelled fields into Extra.
func (x *ModActivityRow) UnmarshalJSON(b []byte) error {
	type alias ModActivityRow
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ModActivityRow(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ModQueueHealth and collects any unmodelled fields into Extra.
func (x *ModQueueHealth) UnmarshalJSON(b []byte) error {
	type alias ModQueueHealth
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ModQueueHealth(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}
