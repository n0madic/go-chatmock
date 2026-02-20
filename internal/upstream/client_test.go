package upstream

import (
	"reflect"
	"testing"
)

func TestMergeIncludesDedupeAndReasoning(t *testing.T) {
	got := mergeIncludes([]string{"foo", "reasoning.encrypted_content", "foo", "bar"}, true)
	want := []string{"foo", "reasoning.encrypted_content", "bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMergeIncludesNoReasoning(t *testing.T) {
	got := mergeIncludes([]string{"foo", "bar", "foo"}, false)
	want := []string{"foo", "bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
