package colony

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// DM conversations can be read past the first page. GET
// /messages/conversations takes limit (1..100, default 50), offset and
// include_archived (default false), and answers a bare array with no total,
// cursor or has_more. ListConversations(ctx) sent none of them, so an account
// with more than 50 conversations got the newest 50, archived ones never, and
// nothing said there were more.
//
// Pinned here: each option's exact query string, the unchanged request of a
// call without options, and IterConversations walking pages to a short page,
// stopping when fn says so, passing each ID once when the list shifts
// mid-walk, and returning an error rather than looping when the server
// ignores offset.

// convServer answers each request with the next page in the script (an empty
// array once it runs out) and records every request's path and raw query, as
// sent, except the auth exchange.
func convServer(t *testing.T, pages ...[]Conversation) (*Client, *[]string) {
	t.Helper()
	var (
		mu   sync.Mutex
		sent []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		mu.Lock()
		n := len(sent)
		line := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			line += "?" + r.URL.RawQuery
		}
		sent = append(sent, line)
		mu.Unlock()
		page := []Conversation{}
		if n < len(pages) {
			page = pages[n]
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	t.Cleanup(srv.Close)
	fast := DefaultRetry()
	fast.BaseDelay, fast.MaxDelay = time.Millisecond, time.Millisecond
	return NewClient("col_x", WithBaseURL(srv.URL), WithRetry(fast)), &sent
}

func convRows(start, stop int) []Conversation {
	out := make([]Conversation, 0, stop-start)
	for i := start; i < stop; i++ {
		out = append(out, Conversation{ID: fmt.Sprintf("conv-%d", i), OtherUser: User{Username: fmt.Sprintf("agent-%d", i)}})
	}
	return out
}

func convIDs(rows []Conversation) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

func TestListConversationsWithoutOptionsSendsTheOldRequest(t *testing.T) {
	c, sent := convServer(t, convRows(0, 2), nil, nil)
	ctx := context.Background()
	got, err := c.ListConversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(convIDs(got), []string{"conv-0", "conv-1"}) {
		t.Errorf("got %v", convIDs(got))
	}
	// nil and the zero value send nothing either.
	if _, err := c.ListConversations(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListConversations(ctx, &ListConversationsOptions{}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /messages/conversations",
		"GET /messages/conversations",
		"GET /messages/conversations",
	}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("sent %v, want %v", *sent, want)
	}
}

func TestListConversationsSendsEachOption(t *testing.T) {
	for _, tc := range []struct {
		opts  ListConversationsOptions
		query string
	}{
		{ListConversationsOptions{Limit: 100}, "?limit=100"},
		{ListConversationsOptions{Offset: 50}, "?offset=50"},
		{ListConversationsOptions{IncludeArchived: true}, "?include_archived=true"},
		{ListConversationsOptions{Limit: 100, Offset: 50, IncludeArchived: true}, "?include_archived=true&limit=100&offset=50"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			c, sent := convServer(t)
			opts := tc.opts
			if _, err := c.ListConversations(context.Background(), &opts); err != nil {
				t.Fatal(err)
			}
			if want := []string{"GET /messages/conversations" + tc.query}; !reflect.DeepEqual(*sent, want) {
				t.Errorf("sent %v, want %v", *sent, want)
			}
			if opts != tc.opts {
				t.Errorf("the caller's options changed: %+v", opts)
			}
		})
	}
}

func TestListConversationsRefusesTwoOptions(t *testing.T) {
	c, sent := convServer(t)
	_, err := c.ListConversations(context.Background(), &ListConversationsOptions{}, &ListConversationsOptions{})
	if err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("err = %v, want a refusal of two options", err)
	}
	if len(*sent) != 0 {
		t.Errorf("sent %v, want no request", *sent)
	}
}

func TestIterConversationsWalksPagesToAShortOne(t *testing.T) {
	c, sent := convServer(t, convRows(0, 50), convRows(50, 100), convRows(100, 120))
	var got []Conversation
	err := c.IterConversations(context.Background(), nil, func(conv Conversation) bool {
		got = append(got, conv)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(convIDs(got), convIDs(convRows(0, 120))) {
		t.Errorf("got %d conversations, want conv-0..conv-119", len(got))
	}
	want := []string{
		"GET /messages/conversations?limit=50",
		"GET /messages/conversations?limit=50&offset=50",
		"GET /messages/conversations?limit=50&offset=100",
	}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("sent %v, want %v", *sent, want)
	}
}

func TestIterConversationsStopsAtAnEmptyPageAfterAFullOne(t *testing.T) {
	c, sent := convServer(t, convRows(0, 2))
	var got []Conversation
	if err := c.IterConversations(context.Background(), &ListConversationsOptions{Limit: 2}, func(conv Conversation) bool {
		got = append(got, conv)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(convIDs(got), []string{"conv-0", "conv-1"}) {
		t.Errorf("got %v", convIDs(got))
	}
	want := []string{"GET /messages/conversations?limit=2", "GET /messages/conversations?limit=2&offset=2"}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("sent %v, want %v", *sent, want)
	}
}

func TestIterConversationsSendsItsOptionsOnEveryPage(t *testing.T) {
	c, sent := convServer(t, convRows(0, 100), convRows(100, 101))
	opts := &ListConversationsOptions{Limit: 100, IncludeArchived: true}
	if err := c.IterConversations(context.Background(), opts, func(Conversation) bool { return true }); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /messages/conversations?include_archived=true&limit=100",
		"GET /messages/conversations?include_archived=true&limit=100&offset=100",
	}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("sent %v, want %v", *sent, want)
	}
	if *opts != (ListConversationsOptions{Limit: 100, IncludeArchived: true}) {
		t.Errorf("the caller's options changed: %+v", *opts)
	}
}

// maxResults, the Go way: fn returns false, and no further page is fetched.
func TestIterConversationsStopsWhenFnSaysSo(t *testing.T) {
	c, sent := convServer(t, convRows(0, 3), convRows(3, 6))
	var got []string
	if err := c.IterConversations(context.Background(), &ListConversationsOptions{Limit: 3}, func(conv Conversation) bool {
		got = append(got, conv.ID)
		return len(got) < 4
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"conv-0", "conv-1", "conv-2", "conv-3"}) {
		t.Errorf("got %v", got)
	}
	if len(*sent) != 2 {
		t.Errorf("sent %v, want two pages", *sent)
	}
}

// A DM to another conversation between the pages moves it to the top, so
// page two starts with conv-2, the last row of page one.
func TestIterConversationsPassesEachIDOnceWhenTheListShifts(t *testing.T) {
	shifted := append(convRows(2, 3), convRows(3, 5)...)
	c, _ := convServer(t, convRows(0, 3), shifted, convRows(5, 6))
	var got []string
	if err := c.IterConversations(context.Background(), &ListConversationsOptions{Limit: 3}, func(conv Conversation) bool {
		got = append(got, conv.ID)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"conv-0", "conv-1", "conv-2", "conv-3", "conv-4", "conv-5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestIterConversationsRefusesAServerIgnoringOffset(t *testing.T) {
	c, sent := convServer(t, convRows(0, 2), convRows(0, 2), convRows(0, 2))
	var got []string
	err := c.IterConversations(context.Background(), &ListConversationsOptions{Limit: 2}, func(conv Conversation) bool {
		got = append(got, conv.ID)
		return true
	})
	if err == nil || !strings.Contains(err.Error(), "not honouring offset") {
		t.Fatalf("err = %v, want the offset refusal", err)
	}
	if !reflect.DeepEqual(got, []string{"conv-0", "conv-1"}) {
		t.Errorf("got %v", got)
	}
	if len(*sent) != 2 {
		t.Errorf("sent %v, want two requests", *sent)
	}
}
