package middleware

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gwmw"
)

func TestInternalRequestOptIn(t *testing.T) {
	use(t,
		file(t, `export function onRequest(body) { body.legacy = true; return body; }`),
		file(t, `export const internalRequests = ["another_kind"];
export function onRequest(body) { body.other = true; return body; }`),
		file(t, `export const internalRequests = ["codex_compaction"];
export function onRequest(body, ctx) { body.kind = ctx.kind; ctx.state.mark = "request-state"; return body; }
export function onProvider(body, ctx) { body.mark = ctx.state.mark + "/" + ctx.provider.id; return body; }`),
	)
	i := info()
	i.Kind = "codex_compaction"
	r := (Chain{}).BeginInternal(i)
	if r == nil {
		t.Fatal("opted-in module did not run")
	}
	defer r.End()
	b, reject := r.Request([]byte(`{"model":"m1"}`))
	if reject != nil || !strings.Contains(string(b), `"kind":"codex_compaction"`) || strings.Contains(string(b), "legacy") || strings.Contains(string(b), "other") {
		t.Fatalf("wrong internal modules: %s %+v", b, reject)
	}
	b = r.(gwmw.Tried).Tried(b, gwmw.To{ID: "a"})
	if !strings.Contains(string(b), "request-state/a") {
		t.Fatalf("request and provider hooks did not share state: %s", b)
	}
	i.Kind = ""
	if r := (Chain{}).BeginInternal(i); r != nil {
		r.End()
		t.Fatal("empty internal kind was accepted")
	}
	i.Kind = "title"
	if r := (Chain{}).BeginInternal(i); r != nil {
		r.End()
		t.Fatal("unlisted internal kind was accepted")
	}
}

func TestInternalProviderOnly(t *testing.T) {
	use(t, file(t, `export const internalRequests = ["codex_compaction"];
export function onProvider(body, ctx) { body.provider = ctx.provider.id; return body; }`))
	i := info()
	i.Kind = "codex_compaction"
	r := (Chain{}).BeginInternal(i)
	if r == nil {
		t.Fatal("provider-only module did not run")
	}
	defer r.End()
	b, reject := r.Request([]byte(`{"model":"m1"}`))
	if reject != nil {
		t.Fatal(reject)
	}
	b = r.(gwmw.Tried).Tried(b, gwmw.To{ID: "a"})
	if !strings.Contains(string(b), `"provider":"a"`) {
		t.Fatalf("provider-only hook not applied: %s", b)
	}
}

func TestInternalRequestHasSeparateState(t *testing.T) {
	use(t, file(t, `export const internalRequests = ["codex_compaction"];
export function onRequest(body, ctx) { ctx.state.value = body.value; }
export function onProvider(body, ctx) { body.value = ctx.state.value; return body; }`))
	agent := Begin(info())
	defer agent.End()
	agent.Request([]byte(`{"value":"agent"}`))
	i := info()
	i.Kind = "codex_compaction"
	internal := (Chain{}).BeginInternal(i)
	if internal == nil {
		t.Fatal("internal request did not run")
	}
	defer internal.End()
	internal.Request([]byte(`{"value":"summary"}`))
	for _, test := range []struct {
		run  gwmw.Tried
		want string
	}{{agent, "agent"}, {internal.(gwmw.Tried), "summary"}} {
		b := test.run.Tried([]byte(`{}`), gwmw.To{ID: "a"})
		if !strings.Contains(string(b), `"value":"`+test.want+`"`) {
			t.Fatalf("request states crossed: %s, want %s", b, test.want)
		}
	}
}
