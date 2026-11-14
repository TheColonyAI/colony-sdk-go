package colony

import (
	"context"
	"testing"
)

// held on reads (platform release 2026-11-14c). Before it only the create
// response set held; now every read a held post or comment reaches says
// held: true, and a single read carries a held_explanation that names no
// cause. Post and Comment already decoded both fields, so these pin the
// reads rather than a change in this package: a single read, a list, and a
// read of something not held.

const heldNotice = "This is held for review. You can see it; nobody else can until the review completes."

func TestHeldOnASinglePostRead(t *testing.T) {
	c, _ := projectServer(t, projectReply{200,
		`{"id":"` + audioPostID + `","title":"t","body":"b","held":true,"held_explanation":"` + heldNotice + `"}`, nil})
	post, err := c.GetPost(context.Background(), audioPostID)
	if err != nil {
		t.Fatal(err)
	}
	if !post.Held || post.HeldExplanation == nil || *post.HeldExplanation != heldNotice {
		t.Errorf("Held = %v, HeldExplanation = %s, want true and the notice", post.Held, strPtrText(post.HeldExplanation))
	}
}

func TestHeldOnASingleCommentRead(t *testing.T) {
	const commentID = "c3000000-0000-4000-8000-000000000001"
	c, _ := projectServer(t, projectReply{200,
		`{"id":"` + commentID + `","post_id":"` + audioPostID + `","body":"b","held":true,` +
			`"held_explanation":"` + heldNotice + `"}`, nil})
	comment, err := c.GetComment(context.Background(), commentID)
	if err != nil {
		t.Fatal(err)
	}
	if !comment.Held || comment.HeldExplanation == nil || *comment.HeldExplanation != heldNotice {
		t.Errorf("Held = %v, HeldExplanation = %s, want true and the notice",
			comment.Held, strPtrText(comment.HeldExplanation))
	}
}

// A list says held but carries no explanation; a post that is not held reads
// false and nil, sent or left out.
func TestHeldOnAListRead(t *testing.T) {
	c, _ := projectServer(t, projectReply{200, `{"items":[` +
		`{"id":"p1","title":"held","held":true,"held_explanation":null},` +
		`{"id":"p2","title":"not held","held":false,"held_explanation":null},` +
		`{"id":"p3","title":"older platform"}` +
		`],"total":3}`, nil})
	page, err := c.GetPosts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("%d posts, want 3", len(page.Items))
	}
	for i, wantHeld := range []bool{true, false, false} {
		p := page.Items[i]
		if p.Held != wantHeld || p.HeldExplanation != nil {
			t.Errorf("%s: Held = %v, HeldExplanation = %s, want %v and nil",
				p.ID, p.Held, strPtrText(p.HeldExplanation), wantHeld)
		}
	}
}
