package main

import "fmt"

const INTERNAL_MAX_KEY = 8

type Node any

type BTreeInternalNode struct {
	nkey  int
	keys  [INTERNAL_MAX_KEY]int32
	child [INTERNAL_MAX_KEY]Node
}

func NewINode() BTreeInternalNode {
	return BTreeInternalNode{}
}

// FindLastLE returns the last index whose key is at most findKey, or -1.
func (node *BTreeInternalNode) FindLastLE(findKey int32) int {
	for i := node.nkey - 1; i >= 0; i-- {
		if node.keys[i] <= findKey {
			return i
		}
	}
	return -1
}

// InsertKV inserts a key and its child in sorted order. The node must have room.
func (node *BTreeInternalNode) InsertKV(key int32, child Node) {
	if node.nkey == len(node.keys) {
		panic("cannot insert into a full internal node")
	}

	pos := node.FindLastLE(key) + 1
	for i := node.nkey; i > pos; i-- {
		node.keys[i] = node.keys[i-1]
		node.child[i] = node.child[i-1]
	}
	node.keys[pos] = key
	node.child[pos] = child
	node.nkey++
}

// Split keeps the left half in node and returns the right half.
func (node *BTreeInternalNode) Split() BTreeInternalNode {
	mid := node.nkey / 2
	keys := node.keys[mid:node.nkey]
	children := node.child[mid:node.nkey]

	right := BTreeInternalNode{nkey: len(keys)}
	copy(right.keys[:], keys)
	copy(right.child[:], children)
	clear(keys)
	clear(children)
	node.nkey = mid
	return right
}

func main() {
	fmt.Println("Hello World")
}
