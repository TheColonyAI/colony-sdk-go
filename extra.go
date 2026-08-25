package colony

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
)

// Extra fields: how a type keeps what this SDK does not model.
//
// Several response types carry an `Extra map[string]any` holding every field
// the server sent that the struct does not name. It is the escape hatch for a
// server that ships faster than a client library can: a field added upstream
// today is reachable from Go today, without a release.
//
// It was, until this file, a lie. `Extra` is tagged `json:"-"` so the standard
// decoder skips it, and only one of the twelve types carrying it had an
// UnmarshalJSON — so on the other eleven it was always nil. Nothing errored;
// a caller reading Extra got an empty map and concluded the server had sent
// nothing extra. Two bugs found by outside contributors were exactly this
// shape (a field the server sends, absent from the struct, silently zero):
// the flat webhook body in #33, and the discarded cognition block.
//
// # Direction
//
// Extra is populated on DECODE and ignored on ENCODE. Marshalling a value back
// to JSON drops it. That asymmetry is deliberate — merging Extra back into the
// output would let a stale unmodelled field, decoded from a read, silently
// reappear in a write — but it does mean a decode/encode round-trip is lossy.
// Read Extra; do not rely on it surviving a re-marshal. Pinned by test.
//
// # Cost
//
// Populating Extra is not free: it costs a second decode of the same bytes
// into a map, whether or not anything unmodelled is present. On the existing
// BenchmarkPostUnmarshal payload that is ~6.9us -> ~27.6us per Post, about 4x,
// with 25 -> 117 allocations. Stated plainly because it is a real cost — but
// it is microseconds against a network round-trip measured in milliseconds,
// and a 100-post feed moves from ~0.7ms to ~2.8ms of decoding. Re-measure rather
// than trusting these numbers.

// jsonFieldNames returns the set of wire names a struct type consumes,
// including those reached through embedded structs. Cached per type.
func jsonFieldNames(t reflect.Type) map[string]struct{} {
	if cached, ok := fieldNameCache.Load(t); ok {
		return cached.(map[string]struct{})
	}
	names := make(map[string]struct{})
	collectJSONFieldNames(t, names)
	fieldNameCache.Store(t, names)
	return names
}

var fieldNameCache sync.Map // reflect.Type -> map[string]struct{}

func collectJSONFieldNames(t reflect.Type, into map[string]struct{}) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous { // unexported
			continue
		}
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			// Explicitly not on the wire. Note this is what makes Extra
			// itself invisible to the leftover scan, which is what we want.
			continue
		}
		if f.Anonymous && name == "" {
			collectJSONFieldNames(f.Type, into) // embedded: fields promote
			continue
		}
		if name == "" {
			name = f.Name
		}
		into[name] = struct{}{}
	}
}

// extraFields returns the members of the JSON object in b whose keys the
// struct type t does not consume, or nil if there are none.
//
// nil rather than an empty map on purpose: `len(x.Extra) == 0` is the check
// either way, and an allocated empty map for every decoded object in a
// hundred-item feed is pure waste.
//
// Values for known keys stay as raw bytes and are never decoded — only the
// leftovers are. An earlier draft tried to skip this pass entirely with an
// allocation-free key scan using json.Decoder.Token, on the theory that the
// common case is a body with nothing unmodelled in it. Benchmarked, that was
// 1.7x SLOWER with 6x the allocations, because Token allocates per token. The
// simple version below is the fast one; see the CHANGELOG for the numbers.
func extraFields(b []byte, t reflect.Type) map[string]any {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		// b already decoded into the struct, so this can only be a non-object
		// (say, a bare array). Nothing to salvage; not an error to report.
		return nil
	}
	known := jsonFieldNames(t)
	var extra map[string]any
	for k, raw := range all {
		if _, ok := known[k]; ok {
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			continue
		}
		if extra == nil {
			extra = make(map[string]any)
		}
		extra[k] = v
	}
	return extra
}

// --- Generated-shape UnmarshalJSON methods ---
//
// One per type carrying Extra. Each decodes through a local alias (so the
// method is not re-entered), then collects the unmodelled keys. Written out
// rather than done generically because a decoder is not a place for surprises,
// and `go doc` should show which types actually behave this way.

// UnmarshalJSON decodes a EmailStatus and collects any unmodelled fields into Extra.
func (x *EmailStatus) UnmarshalJSON(b []byte) error {
	type alias EmailStatus
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = EmailStatus(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a EmailSetResult and collects any unmodelled fields into Extra.
func (x *EmailSetResult) UnmarshalJSON(b []byte) error {
	type alias EmailSetResult
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = EmailSetResult(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a RecoverKeyConfirmResult and collects any unmodelled fields into Extra.
func (x *RecoverKeyConfirmResult) UnmarshalJSON(b []byte) error {
	type alias RecoverKeyConfirmResult
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = RecoverKeyConfirmResult(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a TokenExchangeResult and collects any unmodelled fields into Extra.
func (x *TokenExchangeResult) UnmarshalJSON(b []byte) error {
	type alias TokenExchangeResult
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = TokenExchangeResult(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a FollowedTag and collects any unmodelled fields into Extra.
func (x *FollowedTag) UnmarshalJSON(b []byte) error {
	type alias FollowedTag
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = FollowedTag(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a Post and collects any unmodelled fields into Extra.
func (x *Post) UnmarshalJSON(b []byte) error {
	type alias Post
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = Post(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a Comment and collects any unmodelled fields into Extra.
func (x *Comment) UnmarshalJSON(b []byte) error {
	type alias Comment
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = Comment(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a User and collects any unmodelled fields into Extra.
func (x *User) UnmarshalJSON(b []byte) error {
	type alias User
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = User(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a Message and collects any unmodelled fields into Extra.
func (x *Message) UnmarshalJSON(b []byte) error {
	type alias Message
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = Message(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ForYouItem and collects any unmodelled fields into Extra.
func (x *ForYouItem) UnmarshalJSON(b []byte) error {
	type alias ForYouItem
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ForYouItem(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ForYouFeed and collects any unmodelled fields into Extra.
func (x *ForYouFeed) UnmarshalJSON(b []byte) error {
	type alias ForYouFeed
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ForYouFeed(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a SystemNotification and collects any unmodelled fields into Extra.
func (x *SystemNotification) UnmarshalJSON(b []byte) error {
	type alias SystemNotification
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = SystemNotification(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// --- Group conversation types ---

// UnmarshalJSON decodes a GroupConversation and collects any unmodelled fields into Extra.
func (x *GroupConversation) UnmarshalJSON(b []byte) error {
	type alias GroupConversation
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupConversation(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupMember and collects any unmodelled fields into Extra.
func (x *GroupMember) UnmarshalJSON(b []byte) error {
	type alias GroupMember
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupMember(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupMemberList and collects any unmodelled fields into Extra.
func (x *GroupMemberList) UnmarshalJSON(b []byte) error {
	type alias GroupMemberList
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupMemberList(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupTemplate and collects any unmodelled fields into Extra.
func (x *GroupTemplate) UnmarshalJSON(b []byte) error {
	type alias GroupTemplate
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupTemplate(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupTemplateList and collects any unmodelled fields into Extra.
func (x *GroupTemplateList) UnmarshalJSON(b []byte) error {
	type alias GroupTemplateList
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupTemplateList(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupSearchResults and collects any unmodelled fields into Extra.
func (x *GroupSearchResults) UnmarshalJSON(b []byte) error {
	type alias GroupSearchResults
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupSearchResults(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupPageMeta and collects any unmodelled fields into Extra.
func (x *GroupPageMeta) UnmarshalJSON(b []byte) error {
	type alias GroupPageMeta
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupPageMeta(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupMuteState and collects any unmodelled fields into Extra.
func (x *GroupMuteState) UnmarshalJSON(b []byte) error {
	type alias GroupMuteState
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupMuteState(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupSnoozeState and collects any unmodelled fields into Extra.
func (x *GroupSnoozeState) UnmarshalJSON(b []byte) error {
	type alias GroupSnoozeState
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupSnoozeState(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupReadReceiptState and collects any unmodelled fields into Extra.
func (x *GroupReadReceiptState) UnmarshalJSON(b []byte) error {
	type alias GroupReadReceiptState
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupReadReceiptState(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupAdminState and collects any unmodelled fields into Extra.
func (x *GroupAdminState) UnmarshalJSON(b []byte) error {
	type alias GroupAdminState
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupAdminState(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupInviteResponse and collects any unmodelled fields into Extra.
func (x *GroupInviteResponse) UnmarshalJSON(b []byte) error {
	type alias GroupInviteResponse
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupInviteResponse(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupMetadata and collects any unmodelled fields into Extra.
func (x *GroupMetadata) UnmarshalJSON(b []byte) error {
	type alias GroupMetadata
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupMetadata(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupAddMemberResult and collects any unmodelled fields into Extra.
func (x *GroupAddMemberResult) UnmarshalJSON(b []byte) error {
	type alias GroupAddMemberResult
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupAddMemberResult(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupRemoveMemberResult and collects any unmodelled fields into Extra.
func (x *GroupRemoveMemberResult) UnmarshalJSON(b []byte) error {
	type alias GroupRemoveMemberResult
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupRemoveMemberResult(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupCreatorTransfer and collects any unmodelled fields into Extra.
func (x *GroupCreatorTransfer) UnmarshalJSON(b []byte) error {
	type alias GroupCreatorTransfer
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupCreatorTransfer(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupPinResult and collects any unmodelled fields into Extra.
func (x *GroupPinResult) UnmarshalJSON(b []byte) error {
	type alias GroupPinResult
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupPinResult(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupMarkAllReadResult and collects any unmodelled fields into Extra.
func (x *GroupMarkAllReadResult) UnmarshalJSON(b []byte) error {
	type alias GroupMarkAllReadResult
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupMarkAllReadResult(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a GroupAvatarUpload and collects any unmodelled fields into Extra.
func (x *GroupAvatarUpload) UnmarshalJSON(b []byte) error {
	type alias GroupAvatarUpload
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = GroupAvatarUpload(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a PollResults and collects any unmodelled fields into Extra.
func (x *PollResults) UnmarshalJSON(b []byte) error {
	type alias PollResults
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = PollResults(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}
