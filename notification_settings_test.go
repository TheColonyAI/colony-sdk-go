package colony

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
)

// Notification settings (platform release 2026-11-01b):
// GetNotificationSettings and UpdateNotificationSettings.

// Shaped like a real answer from the platform.
const notificationSettingsBody = `{
	"switches": [
		{"key": "notify_mentions", "on": true, "events": ["mention"], "description": "Someone mentioned you"},
		{"key": "notify_reactions", "on": false, "events": ["post_reaction"], "description": "A reaction"}
	],
	"always_on": [{"event": "security_2fa_disabled", "description": "Two-factor turned off"}],
	"colonies": [{"colony": "findings", "level": "mentions"}],
	"levels": {"all": "every notification", "mentions": "mentions and pings", "nothing": "mentions only"},
	"muted_threads": [{"post_id": "9cb7ff7d-51a0-46c6-acd2-d731164c792a", "title": "A thread"}]
}`

func TestGetNotificationSettings(t *testing.T) {
	c, rec := wikiServer(t, http.StatusOK, notificationSettingsBody)
	out, err := c.GetNotificationSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodGet || rec.path != "/me/notification-settings" || len(rec.body) != 0 {
		t.Errorf("sent %s %s with body %q", rec.method, rec.path, rec.body)
	}
	if len(out.Switches) != 2 || out.Switches[1].Key != "notify_reactions" || out.Switches[1].On {
		t.Errorf("switches %+v", out.Switches)
	}
	if !reflect.DeepEqual(out.Switches[0].Events, []string{"mention"}) {
		t.Errorf("events %v", out.Switches[0].Events)
	}
	if len(out.AlwaysOn) != 1 || out.AlwaysOn[0].Event != "security_2fa_disabled" {
		t.Errorf("always_on %+v", out.AlwaysOn)
	}
	if !reflect.DeepEqual(out.Colonies, []ColonyNotificationLevel{{Colony: "findings", Level: ColonyLevelMentions}}) {
		t.Errorf("colonies %+v", out.Colonies)
	}
	if out.Levels[ColonyLevelNothing] != "mentions only" {
		t.Errorf("levels %v", out.Levels)
	}
	if len(out.MutedThreads) != 1 || out.MutedThreads[0].Title != "A thread" {
		t.Errorf("muted_threads %+v", out.MutedThreads)
	}
}

func TestUpdateNotificationSettingsSendsOnlyWhatIsSet(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		change UpdateNotificationSettingsRequest
		want   map[string]any
	}{
		{"switches", UpdateNotificationSettingsRequest{Switches: map[string]bool{"notify_reactions": false}},
			map[string]any{"switches": map[string]any{"notify_reactions": false}}},
		{"colonies", UpdateNotificationSettingsRequest{Colonies: map[string]string{"findings": ColonyLevelMentions}},
			map[string]any{"colonies": map[string]any{"findings": "mentions"}}},
		{"both", UpdateNotificationSettingsRequest{
			Switches: map[string]bool{"notify_follows": true},
			Colonies: map[string]string{"general": ColonyLevelNothing},
		}, map[string]any{
			"switches": map[string]any{"notify_follows": true},
			"colonies": map[string]any{"general": "nothing"},
		}},
	} {
		c, rec := wikiServer(t, http.StatusOK, notificationSettingsBody)
		out, err := c.UpdateNotificationSettings(ctx, tc.change)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if rec.method != http.MethodPatch || rec.path != "/me/notification-settings" {
			t.Errorf("%s sent %s %s", tc.name, rec.method, rec.path)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.body, &got); err != nil {
			t.Fatalf("%s: body %q: %v", tc.name, rec.body, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: body %v, want %v", tc.name, got, tc.want)
		}
		if len(out.Switches) != 2 {
			t.Errorf("%s: the settings as they now are were not returned: %+v", tc.name, out)
		}
	}
}

func TestUpdateNotificationSettingsRefusesAnEmptyChangeBeforeTheRequest(t *testing.T) {
	for _, change := range []UpdateNotificationSettingsRequest{
		{},
		{Switches: map[string]bool{}},
		{Colonies: map[string]string{}},
	} {
		c, rec := wikiServer(t, http.StatusOK, notificationSettingsBody)
		if _, err := c.UpdateNotificationSettings(context.Background(), change); err == nil {
			t.Errorf("an empty change %+v was accepted", change)
		}
		if rec.method != "" {
			t.Errorf("refused %+v, but the request was still sent", change)
		}
	}
}

func TestUpdateNotificationSettingsReportsARefusalAsAValidationError(t *testing.T) {
	c, _ := wikiServer(t, http.StatusBadRequest,
		`{"detail": {"message": "unknown notification switch(es) ['notify_nope']", "code": "INVALID_INPUT"}}`)
	_, err := c.UpdateNotificationSettings(context.Background(),
		UpdateNotificationSettingsRequest{Switches: map[string]bool{"notify_nope": true}})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("got %T %v, want *ValidationError", err, err)
	}
}
