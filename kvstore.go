package main

import "bytes"

// KV provides a persistent key-value store for byte slices up to eight bytes each.
type KV struct {
	tree *BPTreeDisk
}

func NewKV(fileName string) *KV {
	return &KV{tree: NewBPTreeDisk(fileName)}
}

func (db *KV) Get(key []byte) ([]byte, bool, error) {
	entry, found, err := db.tree.Find(key)
	if err != nil || !found {
		return nil, found, err
	}
	return bytes.Clone(entry.value[maxValueSize-entry.valueLength:]), true, nil
}

func (db *KV) Set(key, value []byte) error {
	return db.tree.Set(key, value)
}

func (db *KV) Del(key []byte) (bool, error) {
	return db.tree.Del(key)
}
