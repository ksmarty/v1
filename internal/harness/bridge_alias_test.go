package harness

import (
	"context"
	"testing"
)

// stubRunner satisfies ToolRunner for the alias tests.
type stubRunner struct{}

func (stubRunner) RunTool(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil }
func (stubRunner) Authorize(context.Context, ToolCall) (string, error)   { return "", nil }

func newAliasBridge() *Bridge {
	return &Bridge{runners: map[string]ToolRunner{}, aliases: map[string]string{}}
}

// A delegated sub-agent runs in its own pi conversation, so the id its tool
// calls carry is not the one the runner was registered under. Every call used to
// be answered "no active turn for tool X", which made delegation useless: the
// sub-agent ran, could not touch a single tool, and returned nothing.
func TestAttachResolvesAChildConversationToItsTurn(t *testing.T) {
	b := newAliasBridge()
	b.runners["parent"] = stubRunner{}
	if b.runner("parent") == nil {
		t.Fatal("the parent's runner did not resolve")
	}
	if b.runner("child") != nil {
		t.Fatal("the child resolved before it was attached")
	}
	b.Attach("child", "parent")
	if b.runner("child") == nil {
		t.Fatal("the child did not resolve to the turn that spawned it")
	}
}

func TestAttachIgnoresUnusablePairs(t *testing.T) {
	b := newAliasBridge()
	b.runners["parent"] = stubRunner{}
	// An empty parent resolves to nothing anyway, and a self-alias is a
	// pointless indirection. Neither should be recorded.
	b.Attach("child", "")
	b.Attach("", "parent")
	b.Attach("self", "self")
	for _, id := range []string{"child", "", "self"} {
		if b.runner(id) != nil {
			t.Fatalf("%q resolved, want nothing", id)
		}
	}
	if len(b.aliases) != 0 {
		t.Fatalf("aliases = %v, want none", b.aliases)
	}
}

// An alias is only meaningful while the turn it points at is running. Keeping it
// would let a later call resolve to a finished turn's runner.
func TestUnregisterDropsTheTurnsAliases(t *testing.T) {
	b := newAliasBridge()
	unregister := b.Register("parent", stubRunner{})
	b.Attach("child", "parent")
	if b.runner("child") == nil {
		t.Fatal("the child did not resolve")
	}
	unregister()
	if b.runner("parent") != nil {
		t.Fatal("the parent's runner outlived the turn")
	}
	if b.runner("child") != nil {
		t.Fatal("the alias outlived the turn")
	}
	if len(b.aliases) != 0 {
		t.Fatalf("aliases = %v, want none", b.aliases)
	}
}
