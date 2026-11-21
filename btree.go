package main

type BTreeLeafNode struct {
	nkey   int
	keys   [INTERNAL_MAX_KEY]int32
	values [INTERNAL_MAX_KEY]int32
}

func NewLNode() BTreeLeafNode {
	return BTreeLeafNode{}
}

func (node *BTreeLeafNode) FindLastLE(key int32) int {
	return findLastLE(node.keys[:node.nkey], key)
}

// InsertKV inserts a key and value in sorted order. The node must have room.
func (node *BTreeLeafNode) InsertKV(key, value int32) {
	if node.nkey == len(node.keys) {
		panic("cannot insert into a full leaf node")
	}

	pos := node.FindLastLE(key) + 1
	for i := node.nkey; i > pos; i-- {
		node.keys[i] = node.keys[i-1]
		node.values[i] = node.values[i-1]
	}
	node.keys[pos] = key
	node.values[pos] = value
	node.nkey++
}

// Split keeps the left half in node and returns the right half.
func (node *BTreeLeafNode) Split() BTreeLeafNode {
	mid := node.nkey / 2
	keys := node.keys[mid:node.nkey]
	values := node.values[mid:node.nkey]

	right := BTreeLeafNode{nkey: len(keys)}
	copy(right.keys[:], keys)
	copy(right.values[:], values)
	clear(keys)
	clear(values)
	node.nkey = mid
	return right
}

type BPTree struct {
	head *BTreeInternalNode
}

func NewBPTree() BPTree {
	return BPTree{head: &BTreeInternalNode{}}
}

func (tree *BPTree) Insert(key, value int32) {
	if tree.head == nil {
		tree.head = &BTreeInternalNode{}
	}
	if tree.head.nkey == 0 {
		leaf := NewLNode()
		leaf.InsertKV(key, value)
		tree.head.InsertKV(key, &leaf)
		return
	}

	right := insertIntoInternal(tree.head, key, value)
	if right != nil {
		root := NewINode()
		root.InsertKV(tree.head.keys[0], tree.head)
		root.InsertKV(right.keys[0], right)
		tree.head = &root
	}
}

// insertIntoInternal returns the new right sibling when node splits.
func insertIntoInternal(node *BTreeInternalNode, key, value int32) *BTreeInternalNode {
	pos := max(0, node.FindLastLE(key))

	// Each parent entry tracks the smallest key in its child.
	switch child := node.child[pos].(type) {
	case *BTreeLeafNode:
		child.InsertKV(key, value)
		node.keys[pos] = child.keys[0]
		if child.nkey == len(child.keys) {
			right := child.Split()
			node.insertAt(pos+1, right.keys[0], &right)
		}
	case *BTreeInternalNode:
		right := insertIntoInternal(child, key, value)
		node.keys[pos] = child.keys[0]
		if right != nil {
			node.insertAt(pos+1, right.keys[0], right)
		}
	default:
		panic("invalid child in B+ tree")
	}

	if node.nkey == len(node.keys) {
		right := node.Split()
		return &right
	}
	return nil
}
