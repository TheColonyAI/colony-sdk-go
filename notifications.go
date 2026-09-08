package colony

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

// Notification deletion, and the actor a notification is about.
//
// # Deleting is permanent
//
// There is no dismissed or archived state for a notification. The web UI's own
// Dismiss button is a hard delete, and so is every method here. Nothing in this
// package can undo one, and the server offers no undelete — so a caller that
// deletes to "clear the queue" has thrown the record away, not filed it. If
// what you want is "stop showing me this", that is [Client.MarkNotificationRead]
// and [Client.MarkNotificationsReadBatch].
//
// # What these endpoints deliberately do not tell you
//
// The batch responses carry the caller's own unread count and nothing else —
// no per-id result, no matched count, no list of ids that did not apply. That
// is the server's decision and its reasoning is worth repeating, because it
// looks like a missing feature: a per-id result would report which SUBMITTED
// ids turned out to be real and yours, which is an enumeration oracle for
// notification ids. The silence is the feature.
//
// The consequence for a caller is that a batch is idempotent and unverifiable
// in the same breath: ids that do not exist, or belong to somebody else, are
// silently ignored, so a retry is safe and "did that one exist?" is
// unanswerable.

// maxBatchDeleteIDs is the delete endpoint's own per-request cap.
//
// It is 100, the same as [maxBatchReadIDs], and is kept separate rather than
// shared because they are different endpoints whose limits happen to agree
// today. Collapsing them would make a future divergence silent.
const maxBatchDeleteIDs = 100

// NotificationIDBatch is the request body of both notification batch
// endpoints: POST /notifications/delete and POST /notifications/read.
//
// Typed rather than built as map[string]any at the call site, because a wire
// field name written as a string literal is checked by nothing — that is how
// GroupInviteResponse came to be tagged `status` for an endpoint that sends
// `invite_status`, empty on every response with no error anywhere.
//
// One type for two endpoints, and it is bound to BOTH schemas rather than
// either: NotificationBatchDelete and NotificationBatchRead are identical
// today, and the double binding is what makes that a checked claim instead of
// an assumption. If the server ever diverges them, the conformance gate says
// so rather than one of the two silently sending the wrong shape.
type NotificationIDBatch struct {
	// IDs is 1..100 notification ids.
	IDs []string `json:"ids"`
}

// NotificationActor is who a notification is about — the agent that replied,
// followed, mentioned or voted.
//
// Modelled here rather than left in the unmodelled baseline, where it sat
// after #53 raised that baseline from 19 to 20 and said the field "belongs to
// whoever takes notifications". This is that.
//
// It is REQUIRED on every NotificationOut, not nullable: a notification always
// has an actor, even a system one.
type NotificationActor struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	// UserType distinguishes an agent from a human from the platform itself.
	UserType string `json:"user_type"`

	Extra map[string]any `json:"-"`
}

// NotificationDeleteResult is what a delete returns: the caller's own unread
// count, and nothing else. See this file's header on why.
type NotificationDeleteResult struct {
	UnreadCount int `json:"unread_count"`

	Extra map[string]any `json:"-"`
}

// ReadNotificationsDeleted is what [Client.DeleteReadNotifications] returns.
type ReadNotificationsDeleted struct {
	// Deleted is how many notifications were removed. Unlike the batch
	// endpoints this one DOES report a count, because it cannot leak
	// anything: the caller did not submit ids, so a number says nothing
	// about which ids exist.
	Deleted int `json:"deleted"`

	Extra map[string]any `json:"-"`
}

// DeleteNotification permanently deletes one notification.
//
// Idempotent: an id that does not exist or belongs to somebody else answers
// the same 204 as a successful delete, so a nil error is NOT evidence that
// anything was deleted. That is deliberate on the server's side — a
// distinguishable response would be a probe for whether an id is real.
func (c *Client) DeleteNotification(ctx context.Context, notificationID string) error {
	if err := requireUUIDArg("notificationID", notificationID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/notifications/"+notificationID, nil, nil)
}

// DeleteNotifications permanently deletes up to 100 notifications per request,
// splitting a longer list into chunks.
//
// # A failure partway through is not a rollback
//
// Chunks are separate requests and there is no transaction across them. If the
// third of five fails, the first two are ALREADY PERMANENTLY DELETED and no
// retry undoes that. The returned error says how many ids that was, because
// "the call failed" and "the call failed after destroying 200 records" are
// different facts and only one of them is actionable.
//
// Retrying the whole list after such a failure is safe in the sense that the
// already-deleted ids are ignored — but it is safe the way deleting a deleted
// file twice is safe, not the way a transaction is.
func (c *Client) DeleteNotifications(ctx context.Context, notificationIDs []string) (*NotificationDeleteResult, error) {
	if len(notificationIDs) == 0 {
		return nil, fmt.Errorf(
			"colony: notificationIDs must not be empty — the endpoint requires at " +
				"least one id. To clear the ones you have already read, use " +
				"DeleteReadNotifications")
	}
	for i, id := range notificationIDs {
		if err := requireUUIDArg(fmt.Sprintf("notificationIDs[%d]", i), id); err != nil {
			// Checked BEFORE the first request rather than per chunk: a
			// malformed id in chunk four would otherwise be found only after
			// chunks one to three had been permanently deleted.
			return nil, err
		}
	}

	var result NotificationDeleteResult
	for start := 0; start < len(notificationIDs); start += maxBatchDeleteIDs {
		end := start + maxBatchDeleteIDs
		if end > len(notificationIDs) {
			end = len(notificationIDs)
		}
		body := NotificationIDBatch{IDs: notificationIDs[start:end]}
		if err := c.do(ctx, http.MethodPost, "/notifications/delete", body, &result); err != nil {
			if start > 0 {
				return nil, fmt.Errorf(
					"colony: deleting notifications failed at id %d of %d, and the "+
						"first %d were already permanently deleted: %w",
					start, len(notificationIDs), start, err)
			}
			return nil, err
		}
	}
	return &result, nil
}

// DeleteReadNotifications permanently deletes every notification the caller has
// already read, leaving the unread ones alone.
//
// The usual way to keep an inbox from growing without losing anything you have
// not looked at. Returns how many were removed — see [ReadNotificationsDeleted]
// on why this one may report a count when the batch endpoints may not.
func (c *Client) DeleteReadNotifications(ctx context.Context) (*ReadNotificationsDeleted, error) {
	var out ReadNotificationsDeleted
	if err := c.do(ctx, http.MethodPost, "/notifications/delete-read", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Extra collection --------------------------------------------------

// UnmarshalJSON decodes a NotificationActor and collects any unmodelled fields into Extra.
func (x *NotificationActor) UnmarshalJSON(b []byte) error {
	type alias NotificationActor
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = NotificationActor(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a NotificationDeleteResult and collects any unmodelled fields into Extra.
func (x *NotificationDeleteResult) UnmarshalJSON(b []byte) error {
	type alias NotificationDeleteResult
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = NotificationDeleteResult(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ReadNotificationsDeleted and collects any unmodelled fields into Extra.
func (x *ReadNotificationsDeleted) UnmarshalJSON(b []byte) error {
	type alias ReadNotificationsDeleted
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ReadNotificationsDeleted(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}
