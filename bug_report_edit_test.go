package colony

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// One bug report: read, correct, retract (platform release 2026-10-31c).

const editBugID = "6f0c9a52-1d3e-4b7a-9c21-5e8f7a6b4c3d"

const editBug = `{"id":"` + editBugID + `","title":"Search ignores the colony filter",` +
	`"description":"GET /search?q=x&colony=findings returns posts from other colonies.",` +
	`"url":null,"status":"open","admin_response":null,` +
	`"created_at":"2026-10-27T22:00:00Z","updated_at":"2026-10-27T22:00:00Z"}`

func TestGetBugReport(t *testing.T) {
	c, rec := wikiServer(t, http.StatusOK, editBug)
	bug, err := c.GetBugReport(context.Background(), editBugID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodGet || rec.path != "/bugs/"+editBugID || bug.Status != BugReportOpen {
		t.Errorf("sent %s %s, got %+v", rec.method, rec.path, bug)
	}
}

func TestUpdateBugReportSendsOnlyWhatIsSet(t *testing.T) {
	desc, clear := "The quoted slug is gigawatt-field-notes.", ""
	for _, tc := range []struct {
		name   string
		change UpdateBugReportRequest
		want   string
	}{
		{"description", UpdateBugReportRequest{Description: &desc}, `{"description":"The quoted slug is gigawatt-field-notes."}`},
		{"remove url", UpdateBugReportRequest{URL: &clear}, `{"url":null}`},
	} {
		c, rec := wikiServer(t, http.StatusOK, editBug)
		if _, err := c.UpdateBugReport(context.Background(), editBugID, tc.change); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if rec.method != http.MethodPatch || rec.path != "/bugs/"+editBugID || string(rec.body) != tc.want {
			t.Errorf("%s: sent %s %s %s", tc.name, rec.method, rec.path, rec.body)
		}
	}
}

func TestUpdateBugReportRefusesBeforeTheRequest(t *testing.T) {
	blank := "   "
	for name, change := range map[string]UpdateBugReportRequest{
		"nothing":     {},
		"blank title": {Title: &blank},
	} {
		c, rec := wikiServer(t, http.StatusOK, editBug)
		if _, err := c.UpdateBugReport(context.Background(), editBugID, change); err == nil || rec.method != "" {
			t.Errorf("%s: err %v, sent %q", name, err, rec.method)
		}
	}
	c, rec := wikiServer(t, http.StatusOK, editBug)
	if _, err := c.GetBugReport(context.Background(), editBugID[:8]); err == nil || rec.method != "" {
		t.Errorf("a truncated id: err %v, sent %q", err, rec.method)
	}
}

func TestRetractBugReportAndItsRefusals(t *testing.T) {
	c, rec := wikiServer(t, http.StatusNoContent, "")
	if err := c.RetractBugReport(context.Background(), editBugID); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodDelete || rec.path != "/bugs/"+editBugID {
		t.Errorf("sent %s %s", rec.method, rec.path)
	}
	c, _ = wikiServer(t, http.StatusConflict, `{"detail":{"message":"reviewed","code":"BUG_REPORT_NOT_EDITABLE","status":"fixed"}}`)
	var conflict *ConflictError
	if err := c.RetractBugReport(context.Background(), editBugID); !errors.As(err, &conflict) {
		t.Errorf("a reviewed report: %T %v", err, err)
	}
	c, _ = wikiServer(t, http.StatusNotFound, `{"detail":{"message":"No bug report of yours has this id.","code":"BUG_REPORT_NOT_FOUND"}}`)
	var notFound *NotFoundError
	if _, err := c.GetBugReport(context.Background(), editBugID); !errors.As(err, &notFound) {
		t.Errorf("someone else's report: %T %v", err, err)
	}
}
