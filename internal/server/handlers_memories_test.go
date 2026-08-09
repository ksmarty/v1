package server

import (
	"fmt"
	"strings"
	"testing"

	"v1/internal/store"
)

func TestMemoryPromptWithinBudget(t *testing.T) {
	mems := []store.Memory{
		{ID: 1, Content: "likes dark themes", Enabled: true},
		{ID: 2, Content: "uses pnpm", Enabled: true},
	}
	got := memoryPrompt(mems)
	if !strings.Contains(got, "[1] likes dark themes") || !strings.Contains(got, "[2] uses pnpm") {
		t.Fatalf("missing memories: %q", got)
	}
	if strings.Contains(got, "omitted") {
		t.Fatalf("unexpected omission note: %q", got)
	}
}

func TestMemoryPromptEmpty(t *testing.T) {
	if got := memoryPrompt(nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestMemoryPromptSkipsDisabled(t *testing.T) {
	mems := []store.Memory{
		{ID: 1, Content: "disabled fact", Enabled: false},
		{ID: 2, Content: "enabled fact", Enabled: true},
	}
	got := memoryPrompt(mems)
	if strings.Contains(got, "disabled fact") {
		t.Fatalf("disabled memory leaked into prompt: %q", got)
	}
	if !strings.Contains(got, "[2] enabled fact") {
		t.Fatalf("enabled memory missing: %q", got)
	}
}

func TestMemoryPromptBudgetKeepsNewest(t *testing.T) {
	var mems []store.Memory
	for i := 1; i <= 60; i++ {
		mems = append(mems, store.Memory{ID: int64(i), Content: fmt.Sprintf("fact %03d %s", i, strings.Repeat("x", 80)), Enabled: true})
	}
	got := memoryPrompt(mems)
	if len(got) > memoryBudgetChars+200 { // header + omission note
		t.Fatalf("prompt too large: %d chars", len(got))
	}
	if !strings.Contains(got, "omitted") {
		t.Fatal("expected an omission note")
	}
	if !strings.Contains(got, "[60]") {
		t.Fatal("newest memory should be kept")
	}
	if strings.Contains(got, "[1] fact 001") {
		t.Fatal("oldest memory should have been dropped")
	}
}
