package colony

// The request recorder: the second of two readers for "does what this SDK sends match the API
// document?". The route guard reads paths at the call site, where a path is a literal. Query keys
// and body fields are often computed (a helper picks colony or colony_id) or built in branches, so
// a source reader cannot see them. This one observes instead: it drives every exported Client method
// against a local server, by reflection, with every option field set, and records each request it
// receives. A comparison against the document runs outside this test (it needs the full document).
//
// It has the opposite blind spot to a source reader: it sees values, but only the branches it
// drives. So it drives twice, once with name-like strings and once with id-shaped ones, and a method
// that sends nothing is reported as unread, not as clean.
//
// Opt-in: it runs only when COLONY_REQUEST_RECORD names an output file.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordedRequest struct {
	Method      string              `json:"method"`
	Path        string              `json:"path"`
	Query       map[string][]string `json:"query"`
	ContentType string              `json:"content_type,omitempty"`
	Body        string              `json:"body,omitempty"`
}

type recordedCall struct {
	Method   string            `json:"method"`
	Mode     string            `json:"mode"`
	Requests []recordedRequest `json:"requests"`
	Err      string            `json:"err,omitempty"`
	Panic    string            `json:"panic,omitempty"`
	Skipped  string            `json:"skipped,omitempty"`
}

const recorderUUID = "0b5e1f3c-0000-4000-8000-0000000000a1"

var (
	ctxType    = reflect.TypeOf((*context.Context)(nil)).Elem()
	readerType = reflect.TypeOf((*io.Reader)(nil)).Elem()
	timeType   = reflect.TypeOf(time.Time{})
)

// recorderFill builds a non-zero value of type t: every exported struct field set, one element in
// every slice and map, pointers allocated. mode "name" fills strings with a name-like value, "id"
// with a UUID, so that code which branches on the shape of a value is driven down both branches.
// With minimal set, optional parts (pointers and maps below the argument itself) stay nil,
// because a client-side check can refuse the all-fields call ("a ban duration on a non-ban action")
// before anything is sent.
func recorderFill(t reflect.Type, mode string, depth int, minimal ...bool) reflect.Value {
	min := len(minimal) > 0 && minimal[0]
	v := reflect.New(t).Elem()
	if depth > 4 {
		return v
	}
	switch {
	case t == ctxType:
		return v
	case t == timeType:
		v.Set(reflect.ValueOf(time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)))
		return v
	case t == readerType:
		v.Set(reflect.ValueOf(io.Reader(strings.NewReader("data"))))
		return v
	}
	if min && depth > 0 && (t.Kind() == reflect.Ptr || t.Kind() == reflect.Map) {
		return v
	}
	switch t.Kind() {
	case reflect.String:
		if mode == "id" {
			v.SetString(recorderUUID)
		} else {
			v.SetString("x1")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(2)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(2)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Ptr:
		p := reflect.New(t.Elem())
		p.Elem().Set(recorderFill(t.Elem(), mode, depth+1, min))
		v.Set(p)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" { // unexported
				continue
			}
			v.Field(i).Set(recorderFill(f.Type, mode, depth+1, min))
		}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			v.SetBytes([]byte("data"))
			break
		}
		s := reflect.MakeSlice(t, 1, 1)
		s.Index(0).Set(recorderFill(t.Elem(), mode, depth+1, min))
		v.Set(s)
	case reflect.Map:
		if t.Key().Kind() == reflect.String {
			m := reflect.MakeMapWithSize(t, 1)
			m.SetMapIndex(reflect.ValueOf("k1").Convert(t.Key()), recorderFill(t.Elem(), mode, depth+1, min))
			v.Set(m)
		}
	case reflect.Interface:
		if t.NumMethod() == 0 {
			v.Set(reflect.ValueOf("x1"))
		}
	}
	return v
}

func TestRecordEveryRequest(t *testing.T) {
	out := os.Getenv("COLONY_REQUEST_RECORD")
	if out == "" {
		t.Skip("set COLONY_REQUEST_RECORD=<file> to record every request the SDK sends")
	}
	calls := recordEveryRequest(t)
	b, err := json.MarshalIndent(calls, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sent := 0
	for _, c := range calls {
		if len(c.Requests) > 0 {
			sent++
		}
	}
	t.Logf("recorded %d calls (%d sent at least one request) to %s", len(calls), sent, out)
}

// recordEveryRequest drives every exported Client method four ways (name-like or id-shaped
// strings, every field set or optional parts left nil) and returns what each call sent.
func recordEveryRequest(t *testing.T) []recordedCall {
	t.Helper()
	var mu sync.Mutex
	var got []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/auth/token" {
			mu.Lock()
			got = append(got, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query(),
				ContentType: r.Header.Get("Content-Type"), Body: string(b)})
			mu.Unlock()
		}
		if r.URL.Path == "/colonies" && r.Method == http.MethodGet {
			// Name lookups resolve, so a method taking a colony name goes on to its own request.
			_, _ = w.Write([]byte(`[{"id":"` + recorderUUID + `","name":"x1"}]`))
			return
		}
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var calls []recordedCall
	for _, drive := range []string{"name/max", "id/max", "name/min", "id/min"} {
		mode, minimal := strings.Split(drive, "/")[0], strings.HasSuffix(drive, "/min")
		c := NewClient("col_recorder_"+strings.ReplaceAll(drive, "/", "_"), WithBaseURL(srv.URL), WithRetry(RetryConfig{}))
		cv := reflect.ValueOf(c)
		ct := cv.Type()
		names := make([]string, 0, ct.NumMethod())
		for i := 0; i < ct.NumMethod(); i++ {
			names = append(names, ct.Method(i).Name)
		}
		sort.Strings(names)
		for _, name := range names {
			m := cv.MethodByName(name)
			mt := m.Type()
			call := recordedCall{Method: name, Mode: drive}
			if mt.NumIn() == 0 || mt.In(0) != ctxType {
				call.Skipped = "no context argument"
				calls = append(calls, call)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			args := []reflect.Value{reflect.ValueOf(ctx)}
			for j := 1; j < mt.NumIn(); j++ {
				in := mt.In(j)
				if mt.IsVariadic() && j == mt.NumIn()-1 {
					in = in.Elem()
				}
				args = append(args, recorderFill(in, mode, 0, minimal))
			}
			mu.Lock()
			start := len(got)
			mu.Unlock()
			func() {
				defer func() {
					if p := recover(); p != nil {
						call.Panic = strings.TrimSpace(strings.SplitN(fmtAny(p), "\n", 2)[0])
					}
				}()
				res := m.Call(args)
				for _, r := range res {
					if r.Kind() == reflect.Chan { // an iterator: read until it closes or a second passes
						timeout := reflect.ValueOf(time.After(time.Second))
						for {
							i, _, ok := reflect.Select([]reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: r}, {Dir: reflect.SelectRecv, Chan: timeout}})
							if i == 1 || !ok {
								break
							}
						}
						continue
					}
					if r.Type().Implements(reflect.TypeOf((*error)(nil)).Elem()) && !r.IsNil() {
						call.Err = r.Interface().(error).Error()
					}
				}
			}()
			cancel()
			mu.Lock()
			call.Requests = append([]recordedRequest(nil), got[start:]...)
			mu.Unlock()
			calls = append(calls, call)
		}
	}
	return calls
}

func fmtAny(p any) string {
	if e, ok := p.(error); ok {
		return e.Error()
	}
	if s, ok := p.(string); ok {
		return s
	}
	return reflect.ValueOf(p).String()
}
