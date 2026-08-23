package server

import (
	"reflect"
	"testing"
)

func TestSortTagsNewestFirst(t *testing.T) {
	in := []string{"latest", "v0.0.10", "v0.0.2", "latest", "v0.0.7", "v0.0.17", "v1.0.0-beta", "v0.0.16", "v0.0.3", "v0.0.1", "v0.0.11"}
	got := append([]string{}, in...)
	sortTags(got)
	// numeric-descending: v0.0.17 > v0.0.11 > v0.0.10 > ... > v0.0.1
	// v1.0.0-beta has a 3-part core (1.0.0) so it sorts above all 0.0.x tags;
	// a pre-release is older than its final release, but there is no v1.0.0
	// here, so it ends up highest. "latest" stays at the end (lexical).
	want := []string{"v1.0.0-beta", "v0.0.17", "v0.0.16", "v0.0.11", "v0.0.10", "v0.0.7", "v0.0.3", "v0.0.2", "v0.0.1", "latest", "latest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sortTags = %v, want %v", got, want)
	}
}

func TestSortTagsPreReleaseAfterFinal(t *testing.T) {
	in := []string{"v1.0.0", "v1.0.0-rc.1"}
	got := append([]string{}, in...)
	sortTags(got)
	want := []string{"v1.0.0", "v1.0.0-rc.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sortTags = %v, want %v", got, want)
	}
}

func TestSortTagsStableDuplicates(t *testing.T) {
	in := []string{"v0.0.5", "v0.0.2", "v0.0.5"}
	got := append([]string{}, in...)
	sortTags(got)
	want := []string{"v0.0.5", "v0.0.5", "v0.0.2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sortTags = %v, want %v", got, want)
	}
}
