//go:build go1.23

package colony

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIterEchoesSeq(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		page++
		if page == 1 {
			_, _ = w.Write([]byte(`{"items":[{"id":"e1"},{"id":"e2"}],"total":3,"has_more":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":"e3"}],"total":3,"has_more":false}`))
	}))
	defer srv.Close()

	var got []string
	for echo, err := range NewClient("col_x", WithBaseURL(srv.URL)).
		IterEchoesSeq(context.Background(), &IterEchoesOptions{PageSize: 50}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, echo.ID)
	}
	if strings.Join(got, ",") != "e1,e2,e3" {
		t.Errorf("got %v", got)
	}
}

// Breaking out of the range must stop the iterator rather than leave it
// paginating a server that always says has_more.
func TestIterEchoesSeqStopsOnBreak(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		requests++
		_, _ = w.Write([]byte(`{"items":[{"id":"a"},{"id":"b"}],"total":999,"has_more":true}`))
	}))
	defer srv.Close()

	n := 0
	for range NewClient("col_x", WithBaseURL(srv.URL)).
		IterEchoesSeq(context.Background(), nil) {
		n++
		break
	}
	if n != 1 {
		t.Errorf("yielded %d after break", n)
	}
	if requests != 1 {
		t.Errorf("made %d page requests after a break on the first item", requests)
	}
}

func TestIterEchoesSeqPropagatesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"boom"}`))
	}))
	defer srv.Close()

	saw := false
	c := NewClient("col_x", WithBaseURL(srv.URL), WithRetry(RetryConfig{}))
	for _, err := range c.IterEchoesSeq(context.Background(), nil) {
		if err != nil {
			saw = true
		}
	}
	if !saw {
		t.Error("iterator swallowed a 500")
	}
}
