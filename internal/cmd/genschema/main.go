// Command genschema extracts the response schemas this package models from the
// Colony API's OpenAPI document, and writes them to testdata/.
//
// Why this exists. Every struct in this package is a claim about what the
// server sends, and until now nothing checked those claims. Four separate bugs
// in one week were the same shape — a field modelled with the wrong type, the
// wrong name, or no counterpart on the wire at all:
//
//   - CognitionChallenge.Difficulty typed string; the server sends an integer,
//     so a real challenged create response failed to decode.
//   - GroupInviteResponse tagged `status`; the server sends `invite_status`,
//     so the struct's only field was empty on every response.
//   - GroupSearchResults wrong in every field, so the method returned an empty
//     value on success.
//   - WebhookEnvelope expecting a nested payload the server sends flat (#33).
//
// None of them errored. All four are mechanically detectable against
// https://thecolony.ai/openapi.json, which is public and needs no credentials.
//
// Usage:
//
//	go generate ./...            # or: go run ./internal/cmd/genschema
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const specURL = "https://thecolony.ai/openapi.json"

// wanted is the set of schema names extracted into testdata.
//
// This comment used to say "it mirrors the mapping in schema_conformance_test.go;
// the test fails if the two drift". That was FALSE — no test referenced this
// variable at all. What actually happened was one-directional: a binding naming
// a schema absent from the snapshot errored, so a MISSING entry was caught and
// an ORPHAN was not. There was one: MessageAttachmentOut, extracted on every
// regeneration and bound by nothing, left behind when that binding was corrected
// to AttachmentUploadOut. Removed here.
//
// TestWantedMatchesTheBindings now asserts both directions, so the claim below
// is true rather than asserted. A comment describing a check that does not exist
// is worse than no comment: it is why nobody looked.
var wanted = []string{
	"CursorPaginatedList_PostOut_",
	"PaginatedList_DirectoryUserOut_",
	"CognitionAnswerOut",
	"CognitionChallengeOut",
	"GroupAddMemberOut",
	"GroupInviteResponseOut",
	"GroupMembersListOut",
	"GroupRemoveMemberOut",
	"GroupSearchOut",
	"GroupSetAdminOut",
	"GroupTemplatesListOut",
	"TwoFactorStatusResponse",
	"VaultFileInfo",
	"VaultStatusResponse",
	"BootstrapProfile",
	"Capability",
	"ClaimOut",
	"ColdBudgetWindow",
	"CommentOut",
	"ConversationDetail",
	"ConversationHistoryOut",
	"ConversationOut",
	"ConversationTailOut",
	"DetailResult",
	"DmSpamMarkOut",
	"EchoOut",
	"EchoPost",
	"ForYouFeedOut",
	"ForYouItemOut",
	"AttachmentUploadOut",
	"MessageEditVersion",
	"MessageOut",
	"MessageReactionOut",
	"MessageReadsOut",
	"NotificationOut",
	"PageMeta",
	"PollResults",
	"PostOut",
	"ReportOut",
	"RotateKeyResponse",
	"SavedMessageEntry",
	"SavedMessagesOut",
	"SearchResults",
	"SystemNotificationOut",
	"TrustLevelOut",
	"UnreadCountOut",
	"UserOut",
	"VaultSearchResult",
	"WebhookOut",
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "genschema:", err)
		os.Exit(1)
	}
}

func run() error {
	resp, err := http.Get(specURL)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", specURL, resp.StatusCode)
	}
	var spec struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
		// Paths is read so a binding can name an OPERATION rather than a
		// schema. Name matching reaches only 14 of the 69 types that were
		// unbound when issue #49 was filed; the other 55 have schemas whose
		// names do not line up with the Go type at all. GroupSearchResults is
		// the case that proves it: its schema is GroupSearchOut, no heuristic
		// finds that, and it is the struct #48's own header calls "wrong in
		// every field".
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Content map[string]struct {
					Schema struct {
						Ref string `json:"$ref"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		return err
	}
	all := spec.Components.Schemas
	if len(all) == 0 {
		// An empty document would rewrite testdata as empty and the offline
		// test would then agree with it perfectly.
		return fmt.Errorf("spec declares 0 schemas — refusing to rewrite")
	}

	out := map[string]json.RawMessage{}
	var missing []string
	for _, name := range wanted {
		s, ok := all[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		out[name] = s
	}
	if len(missing) > 0 {
		// A schema that vanished from the API is a finding, not something to
		// skip quietly and write a smaller file.
		sort.Strings(missing)
		return fmt.Errorf("the spec no longer declares %d wanted schema(s): %v "+
			"— investigate before regenerating", len(missing), missing)
	}

	// Pull in the schemas the wanted ones $ref, so the checker can resolve a
	// reference instead of assuming it is an object. PostType and UserType
	// are string enums behind a $ref; assuming object reports them as type
	// errors.
	refs := map[string]json.RawMessage{}
	for _, raw := range out {
		for _, name := range refNames(raw) {
			if s, ok := all[name]; ok {
				refs[name] = s
			}
		}
	}

	// operations maps "METHOD /path" to the schema its success response
	// declares, so a binding can be stated as the endpoint it came from. An
	// endpoint is a fact about where the client actually goes; a schema name
	// is a fact about what the server team called something.
	ops := map[string]string{}
	for path, byMethod := range spec.Paths {
		for method, op := range byMethod {
			for _, code := range []string{"200", "201"} {
				r, ok := op.Responses[code]
				if !ok {
					continue
				}
				c, ok := r.Content["application/json"]
				if !ok || c.Schema.Ref == "" {
					continue
				}
				name := c.Schema.Ref
				if i := strings.LastIndex(name, "/"); i >= 0 {
					name = name[i+1:]
				}
				ops[strings.ToUpper(method)+" "+path] = name
				break
			}
		}
	}
	if len(ops) == 0 {
		return fmt.Errorf("spec declares 0 resolvable operations — refusing to rewrite")
	}

	doc := struct {
		FetchedAt  string                     `json:"fetched_at"`
		Source     string                     `json:"source"`
		Count      int                        `json:"count"`
		Schemas    map[string]json.RawMessage `json:"schemas"`
		Refs       map[string]json.RawMessage `json:"refs"`
		Operations map[string]string          `json:"operations"`
	}{time.Now().UTC().Format(time.RFC3339), specURL, len(out), out, refs, ops}

	b, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile("testdata/openapi_schemas.json", append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %d schemas (+%d referenced) of %d in the document, and %d operations\n",
		len(out), len(refs), len(all), len(ops))
	return nil
}

// refNames returns every schema name a raw schema $refs, at any depth.
func refNames(raw json.RawMessage) []string {
	var seen []string
	for _, m := range refPattern.FindAllStringSubmatch(string(raw), -1) {
		seen = append(seen, m[1])
	}
	return seen
}

var refPattern = regexp.MustCompile(`"\$ref"\s*:\s*"#/components/schemas/([^"]+)"`)
