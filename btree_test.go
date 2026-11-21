package main

import (
	"cmp"
	"slices"
	"testing"
)

func TestLeafNodeInsertAndSplit(t *testing.T) {
	leaf := NewLNode()
	for _, key := range []int32{3, 10, 5, -2, 0} {
		leaf.InsertKV(key, key*10)
	}
	if got := leaf.FindLastLE(4); got != 2 {
		t.Fatalf("FindLastLE(4) = %d, want 2", got)
	}

	right := leaf.Split()
	if got := leaf.keys[:leaf.nkey]; !slices.Equal(got, []int32{-2, 0}) {
		t.Errorf("left keys = %v, want [-2 0]", got)
	}
	if got := leaf.values[:leaf.nkey]; !slices.Equal(got, []int32{-20, 0}) {
		t.Errorf("left values = %v, want [-20 0]", got)
	}
	if got := right.keys[:right.nkey]; !slices.Equal(got, []int32{3, 5, 10}) {
		t.Errorf("right keys = %v, want [3 5 10]", got)
	}
	if got := right.values[:right.nkey]; !slices.Equal(got, []int32{30, 50, 100}) {
		t.Errorf("right values = %v, want [30 50 100]", got)
	}
}

type leafEntry struct {
	key, value int32
}

func TestBPTreeInsertAcrossRootSplits(t *testing.T) {
	tree := NewBPTree()
	var want []leafEntry
	for i := range int32(512) {
		key := (i*197)%512 - 256
		tree.Insert(key, i)
		want = append(want, leafEntry{key, i})
	}
	for i := range int32(32) {
		tree.Insert(0, -i)
		want = append(want, leafEntry{0, -i})
	}
	tree.Insert(-1000, -1)
	want = append(want, leafEntry{-1000, -1})
	slices.SortStableFunc(want, func(a, b leafEntry) int { return cmp.Compare(a.key, b.key) })

	var got []leafEntry
	_, depth := collectLeafEntries(t, tree.head, &got)
	if depth < 3 {
		t.Fatalf("leaf depth = %d, want at least 3 after repeated root splits", depth)
	}
	if len(got) != len(want) {
		t.Fatalf("tree holds %d entries, want %d", len(got), len(want))
	}
	for i, entry := range want {
		if got[i] != entry {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], entry)
		}
	}
}

func collectLeafEntries(t *testing.T, node Node, entries *[]leafEntry) (minKey int32, depth int) {
	t.Helper()
	switch node := node.(type) {
	case *BTreeLeafNode:
		if node.nkey <= 0 || node.nkey >= INTERNAL_MAX_KEY {
			t.Fatalf("leaf holds %d entries after insertion", node.nkey)
		}
		for i := range node.nkey {
			*entries = append(*entries, leafEntry{node.keys[i], node.values[i]})
		}
		return node.keys[0], 0
	case *BTreeInternalNode:
		if node.nkey <= 0 || node.nkey >= INTERNAL_MAX_KEY {
			t.Fatalf("internal node holds %d children after insertion", node.nkey)
		}
		for i := range node.nkey {
			childMin, childDepth := collectLeafEntries(t, node.child[i], entries)
			if node.keys[i] != childMin {
				t.Fatalf("separator %d = %d, want child minimum %d", i, node.keys[i], childMin)
			}
			if i == 0 {
				depth = childDepth + 1
			} else if depth != childDepth+1 {
				t.Fatal("children of the same node lead to leaves at different depths")
			}
		}
		return node.keys[0], depth
	default:
		t.Fatalf("unexpected tree node %T", node)
		return 0, 0
	}
}
