package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The internal summary uses the opted-in request hooks, independently on
// every provider and retry; reply hooks must not rewrite the summary.
func TestCodexCompactMiddleware(t *testing.T) {
	for _, test := range []struct {
		name, optIn, model, path string
		firstStatus              int
		filtered                 bool
	}{
		{"direct", `export const internalRequests = ["codex_compaction"];`, "a/m1", CodexPath + "/responses", 200, true},
		{"public endpoint", `export const internalRequests = ["codex_compaction"];`, "a/m1", "/v1/responses", 200, true},
		{"group fallback", `export const internalRequests = ["codex_compaction"];`, "group/g", CodexPath + "/responses", 503, true},
		{"plain retry", `export const internalRequests = ["codex_compaction"];`, "a/m1", CodexPath + "/responses", 404, true},
		{"legacy unchanged", "", "a/m1", CodexPath + "/responses", 200, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fresh(t)
			var mu sync.Mutex
			var sent []struct{ provider, body string }
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				id := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0]
				mu.Lock()
				sent = append(sent, struct{ provider, body string }{id, string(b)})
				first := len(sent) == 1
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if first && test.firstStatus != 200 {
					w.WriteHeader(test.firstStatus)
					io.WriteString(w, `{"error":{"message":"synthetic refusal"}}`)
					return
				}
				io.WriteString(w, `{"id":"resp_fixture","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Summary survives."}]}]}`)
			}))
			defer up.Close()
			for _, id := range []string{"a", "b"} {
				if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "fixture", Models: []string{"m1"}, Responses: up.URL + "/" + id}); err != nil {
					t.Fatal(err)
				}
			}
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m1", "b/m1"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			onMiddleware(t, test.optIn+`
export function onRequest(body, ctx) {
  if (ctx.kind !== "codex_compaction") throw new Error("wrong kind");
  ctx.state.mark = "summary request preflight";
  return body;
}
export function onProvider(body, ctx) {
  if (ctx.provider.id !== "a") return;
  body.input = body.input.filter(item => item.type !== "reasoning");
  body.instructions = ctx.state.mark;
  return body;
}
export function onEvent() { return null; }
export function onResponse() { return {error: "must not replace the internal summary"}; }
`)
			body := `{"model":"` + test.model + `","stream":true,"store":false,"instructions":"original", "input":[{"type":"reasoning","id":"rs_fixture","encrypted_content":"foreign-sealed-history"},{"type":"message","role":"user","content":"Remember the task and tool results."},{"type":"compaction_trigger"}]}`
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", test.path, strings.NewReader(body))
			req.Header.Set("User-Agent", "codex_cli_rs/0.165.0")
			New().Handler().ServeHTTP(rec, req)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), magpieCompaction) {
				t.Fatalf("summary failed: %d %s", rec.Code, rec.Body.String())
			}
			mu.Lock()
			defer mu.Unlock()
			wantCalls := 1
			if test.firstStatus != 200 {
				wantCalls = 2
			}
			if len(sent) != wantCalls {
				t.Fatalf("got %d upstream calls, want %d: %+v", len(sent), wantCalls, sent)
			}
			for _, call := range sent {
				filtered := test.filtered && call.provider == "a"
				if strings.Contains(call.body, "summary request preflight") != filtered {
					t.Errorf("provider %s request hook not applied independently: %s", call.provider, call.body)
				}
				if strings.Contains(call.body, "foreign-sealed-history") == filtered {
					t.Errorf("provider %s received wrong encrypted history: %s", call.provider, call.body)
				}
				if !strings.Contains(call.body, "Remember the task and tool results.") {
					t.Errorf("readable context lost: %s", call.body)
				}
				var decoded map[string]any
				if err := json.Unmarshal([]byte(call.body), &decoded); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCodexCompactMiddlewareReject(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("rejected compact reached upstream")
		w.WriteHeader(500)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "a", Key: "fixture", Models: []string{"m1"}, Responses: up.URL}); err != nil {
		t.Fatal(err)
	}
	onMiddleware(t, `
export const internalRequests = ["codex_compaction"];
export function onRequest(body, ctx) { ctx.reject(409, "nonportable history; nothing discarded"); }
`)
	code, body := codexPost(t, `{"model":"a/m1","stream":true,"input":[{"type":"message","role":"user","content":"Keep this."},{"type":"compaction_trigger"}]}`)
	if code != 409 || !strings.Contains(body, "nonportable history; nothing discarded") {
		t.Fatalf("compact rejection lost: %d %s", code, body)
	}
}
