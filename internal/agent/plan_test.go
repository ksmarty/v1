package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"v1/internal/store"
)

// planArgs wraps a plan document into the {"plan": ...} arguments a_tool
// expects, without hand-escaped strings.
func planArgs(plan string) string {
	b, _ := json.Marshal(map[string]string{"plan": plan})
	return string(b)
}

func TestMakePlanValid(t *testing.T) {
	e := newTestExecutor(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e.Store = st

	plan := `{"goal":"build a todo app","features":[{"id":"f1","description":"scaffold vite","depends_on":[]},{"id":"f2","description":"todo CRUD","depends_on":["f1"]}],"invariants":["no secrets in client"],"checkpoints":[{"step":1,"action":"install","verification":"npm install succeeds"}],"estimated_turns":4}`
	res := executeContract(t, e, context.Background(), "make_plan", planArgs(plan))
	if res["ok"] != true {
		t.Fatalf("make_plan failed: %v", res)
	}
	got, ok, err := st.GetPlan("testproj")
	if err != nil || !ok {
		t.Fatalf("plan not stored: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(got, "build a todo app") {
		t.Fatalf("stored plan = %q", got)
	}
}

func TestMakePlanValidation(t *testing.T) {
	e := newTestExecutor(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e.Store = st

	cases := []struct {
		name string
		plan string
	}{
		{"not JSON", "not json at all"},
		{"missing goal", `{"features":[{"id":"f1","description":"x"}],"estimated_turns":2}`},
		{"no features", `{"goal":"g","estimated_turns":2}`},
		{"bad dependency", `{"goal":"g","features":[{"id":"f1","description":"x","depends_on":["nope"]}],"estimated_turns":2}`},
	}

	for _, tc := range cases {
		res := executeContract(t, e, context.Background(), "make_plan", planArgs(tc.plan))
		env := toolErrorOf(t, res)
		if env["type"] != "PLAN_INVALID" {
			t.Fatalf("%s: type = %v, want PLAN_INVALID (env %v)", tc.name, env["type"], env)
		}
		// Nothing invalid may be stored.
		if _, ok, err := st.GetPlan("testproj"); err != nil || ok {
			t.Fatalf("%s: an invalid plan was stored (ok=%v err=%v)", tc.name, ok, err)
		}
	}
}

func TestRememberCategoryAndImportance(t *testing.T) {
	e := newTestExecutor(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e.Store = st

	res := executeContract(t, e, context.Background(), "remember", `{"content":"User prefers Tailwind","category":"preference","importance":2}`)
	if res["ok"] != true {
		t.Fatalf("remember failed: %v", res)
	}
	mems, err := st.ListMemories("testproj")
	if err != nil || len(mems) != 1 {
		t.Fatalf("memories = %v err=%v", mems, err)
	}
	if mems[0].Category != "preference" || mems[0].Importance != 2 {
		t.Fatalf("memory = %+v", mems[0])
	}
	// Bad category -> structured error.
	res = executeContract(t, e, context.Background(), "remember", `{"content":"x","category":"banana"}`)
	env := toolErrorOf(t, res)
	if env["type"] != "BAD_ARGUMENT" {
		t.Fatalf("type = %v, want BAD_ARGUMENT", env["type"])
	}
}
