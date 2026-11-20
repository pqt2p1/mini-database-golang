package main

import (
	"slices"
	"testing"
)

func TestFindLastLE(t *testing.T) {
	node := NewINode()
	if got := node.FindLastLE(0); got != -1 {
		t.Fatalf("empty node: got %d, want -1", got)
	}

	node.InsertKV(3, nil)
	node.InsertKV(5, nil)
	node.InsertKV(10, nil)
	for _, tt := range []struct {
		key  int32
		want int
	}{
		{2, -1},
		{3, 0},
		{8, 1},
		{10, 2},
		{99, 2},
	} {
		if got := node.FindLastLE(tt.key); got != tt.want {
			t.Errorf("FindLastLE(%d) = %d, want %d", tt.key, got, tt.want)
		}
	}
}

func TestInternalNodeInsertAndSplit(t *testing.T) {
	node := NewINode()
	for _, key := range []int32{3, 10, 5, 12, 1, 8, 15, 7} {
		// Give each key a distinct child so the test catches misaligned pairs.
		child := &BTreeInternalNode{nkey: 1, keys: [INTERNAL_MAX_KEY]int32{key}}
		node.InsertKV(key, child)
	}
	assertInternalEntries(t, node, []int32{1, 3, 5, 7, 8, 10, 12, 15})

	right := node.Split()
	assertInternalEntries(t, node, []int32{1, 3, 5, 7})
	assertInternalEntries(t, right, []int32{8, 10, 12, 15})
	for i := node.nkey; i < len(node.child); i++ {
		if node.child[i] != nil {
			t.Errorf("left node still holds a child at index %d after split", i)
		}
	}
}

func assertInternalEntries(t *testing.T, node BTreeInternalNode, want []int32) {
	t.Helper()
	if got := node.keys[:node.nkey]; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for i, key := range want {
		child, ok := node.child[i].(*BTreeInternalNode)
		if !ok || child == nil || child.keys[0] != key {
			t.Errorf("child[%d] = %v, want child for key %d", i, node.child[i], key)
		}
	}
}
