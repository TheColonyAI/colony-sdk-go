//go:build go1.23

package colony

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// IterPostsSeq and IterCommentsSeq had ZERO test coverage before this file,
// while the README recommends them as the idiomatic form. They compiled in the
// 1.23 and 1.24 CI matrix, which is why it read as fine.
//
// The tests below are DIFFERENTIAL where they can be: the Seq iterator and the
// channel iterator are separate implementations of one contract, so the useful
// question is not "does Seq work" but "do the two agree". A per-implementation
// test lets them drift apart while both stay green.

func seqPostIDs(t *testing.T, c *Client, opts *IterPostsOptions) []string {
	t.Helper()
	var got []string
	for p, err := range c.IterPostsSeq(context.Background(), opts) {
		if err != nil {
			t.Fatalf("IterPostsSeq: %v", err)
		}
		got = append(got, p.ID)
	}
	return got
}

func chanPostIDs(t *testing.T, c *Client, opts *IterPostsOptions) []string {
	t.Helper()
	var got []string
	for res := range c.IterPosts(context.Background(), opts) {
		if res.Err != nil {
			t.Fatalf("IterPosts: %v", res.Err)
		}
		got = append(got, res.Value.ID)
	}
	return got
}

func TestPostIteratorsAgree(t *testing.T) {
	scripts := map[string][]string{
		"short page with has_more": {
			`{"items":[{"id":"p1"},{"id":"p2"}],"total":3,"has_more":true}`,
			`{"items":[{"id":"p3"}],"total":3,"has_more":false}`,
		},
		"has_more absent, legacy heuristic": {
			`{"items":[` + strings.TrimSuffix(strings.Repeat(`{"id":"p"},`, 20), ",") + `],"total":21}`,
			`{"items":[{"id":"last"}],"total":21}`,
		},
		"single empty page": {
			`{"items":[],"total":0,"has_more":false}`,
		},
		"server contradicts itself: empty but has_more": {
			`{"items":[],"total":9,"has_more":true}`,
		},
	}
	for name, pages := range scripts {
		t.Run(name, func(t *testing.T) {
			opts := &IterPostsOptions{PageSize: 20}
			a := seqPostIDs(t, (&pagerServer{pages: pages}).start(t), opts)
			b := chanPostIDs(t, (&pagerServer{pages: pages}).start(t), opts)
			if strings.Join(a, ",") != strings.Join(b, ",") {
				t.Errorf("iterators disagree:\n  Seq     = %v\n  channel = %v", a, b)
			}
		})
	}
}

func TestCommentIteratorsAgree(t *testing.T) {
	pages := []string{
		`{"items":[{"id":"c1"}],"total":2,"page":1,"has_more":true}`,
		`{"items":[{"id":"c2"}],"total":2,"page":2,"has_more":false}`,
	}
	var seq []string
	for cm, err := range (&pagerServer{pages: pages}).start(t).
		IterCommentsSeq(context.Background(), "p1", 0) {
		if err != nil {
			t.Fatal(err)
		}
		seq = append(seq, cm.ID)
	}
	var ch []string
	for res := range (&pagerServer{pages: pages}).start(t).
		IterComments(context.Background(), "p1", 0) {
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		ch = append(ch, res.Value.ID)
	}
	if strings.Join(seq, ",") != strings.Join(ch, ",") {
		t.Errorf("iterators disagree:\n  Seq     = %v\n  channel = %v", seq, ch)
	}
	if strings.Join(seq, ",") != "c1,c2" {
		t.Errorf("both agreed on the wrong answer: %v", seq)
	}
}

// Breaking out of a range must stop the walk. A Seq that ignores yield's
// return keeps paginating a server that always says has_more — invisible in
// the results, visible only in the request count.
func TestSeqIteratorsStopOnBreak(t *testing.T) {
	always := []string{`{"items":[{"id":"a"},{"id":"b"}],"total":999,"has_more":true}`}

	t.Run("posts", func(t *testing.T) {
		srv := &pagerServer{pages: always}
		n := 0
		for range srv.start(t).IterPostsSeq(context.Background(), &IterPostsOptions{PageSize: 2}) {
			n++
			break
		}
		if n != 1 {
			t.Errorf("yielded %d after break", n)
		}
		if got := atomic.LoadInt32(&srv.requests); got != 1 {
			t.Errorf("made %d page requests after breaking on the first item, want 1", got)
		}
	})

	t.Run("comments", func(t *testing.T) {
		srv := &pagerServer{pages: []string{
			`{"items":[{"id":"c1"},{"id":"c2"}],"total":999,"page":1,"has_more":true}`,
		}}
		n := 0
		for range srv.start(t).IterCommentsSeq(context.Background(), "p1", 0) {
			n++
			break
		}
		if n != 1 {
			t.Errorf("yielded %d after break", n)
		}
		if got := atomic.LoadInt32(&srv.requests); got != 1 {
			t.Errorf("made %d page requests after breaking on the first item, want 1", got)
		}
	})
}

func TestSeqIteratorsRespectMaxResults(t *testing.T) {
	srv := &pagerServer{pages: []string{
		`{"items":[{"id":"a"},{"id":"b"},{"id":"c"}],"total":99,"has_more":true}`,
	}}
	n := 0
	for range srv.start(t).IterPostsSeq(context.Background(),
		&IterPostsOptions{PageSize: 3, MaxResults: 2}) {
		n++
	}
	if n != 2 {
		t.Errorf("yielded %d, want 2", n)
	}
}

func TestSeqIteratorsPropagateErrors(t *testing.T) {
	// A non-envelope body would decode to an empty page and end the walk
	// quietly, so the error path needs a real error status to be exercised.
	errClient := errorServer(t, 500, `{"detail":"boom"}`)

	saw := false
	for _, err := range errClient.IterPostsSeq(context.Background(), nil) {
		if err != nil {
			saw = true
		}
	}
	if !saw {
		t.Error("IterPostsSeq swallowed a 500")
	}

	saw = false
	for _, err := range errClient.IterCommentsSeq(context.Background(), "p1", 0) {
		if err != nil {
			saw = true
		}
	}
	if !saw {
		t.Error("IterCommentsSeq swallowed a 500")
	}
}

// Cancelling the context must end the walk rather than spin.
func TestSeqIteratorsHonourContextCancellation(t *testing.T) {
	srv := &pagerServer{pages: []string{
		`{"items":[{"id":"a"},{"id":"b"}],"total":999,"has_more":true}`,
	}}
	c := srv.start(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n := 0
	for _, err := range c.IterPostsSeq(ctx, &IterPostsOptions{PageSize: 2}) {
		n++
		if n == 1 {
			cancel()
		}
		if err != nil {
			break
		}
		if n > 100 {
			t.Fatal("iterator did not stop after the context was cancelled")
		}
	}
}
