//go:build go1.23

package colony

import (
	"context"
	"testing"
)

// The iter.Seq2 twin of TestIterPostsPassesMemberColonies. IterPostsSeq copies
// its options field by field, separately from IterPosts, so it is checked on
// its own: a field added to one copy and not the other is silently dropped.
func TestIterPostsSeqPassesMemberColonies(t *testing.T) {
	c, reqs := coloniesServer(t, okReply(`{"items":[],"total":0,"has_more":false}`))
	for _, err := range c.IterPostsSeq(context.Background(), &IterPostsOptions{MemberColonies: boolPtr(false)}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertParam(t, (*reqs)[0], "/posts", "member_colonies", "false")
}
