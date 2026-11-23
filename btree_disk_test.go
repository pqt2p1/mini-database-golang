package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLeafPageBinaryFormat(t *testing.T) {
	page := NewLPage()
	page.InsertKV(NewKeyValFromInt(-2, 258))
	wire, err := hex.DecodeString("020000000000000000" + "0001" + "00080008" +
		"fffffffffffffffe" + "0000000000000102")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := page.MarshalBinary()
	if err != nil || !bytes.Equal(encoded, wire) {
		t.Fatalf("encoded leaf = %x, want %x (err: %v)", encoded, wire, err)
	}
	var decoded BTreeLeafPage
	if err := decoded.UnmarshalBinary(wire); err != nil || decoded != page {
		t.Fatalf("leaf did not round trip: %v", err)
	}
	for i := range len(wire) {
		if err := decoded.UnmarshalBinary(wire[:i]); err == nil || decoded != page {
			t.Fatalf("truncation at byte %d: err = %v, page changed = %t", i, err, decoded != page)
		}
	}
}

func TestDiskInsertAfterReopenAndRootSplits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tree.db")
	tree := NewBPTreeDisk(path)
	var want []KeyVal
	insert := func(key, value int64) {
		t.Helper()
		if err := tree.Insert(key, value); err != nil {
			t.Fatalf("insert (%d, %d): %v", key, value, err)
		}
		want = append(want, NewKeyValFromInt(key, value))
	}
	for i := range int64(180) {
		if i == 90 {
			// Simulate an append that stopped partway through an unreachable page.
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := file.Write([]byte{1, 2, 3})
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("append partial page: write = %v, close = %v", writeErr, closeErr)
			}
			tree = NewBPTreeDisk(path)
		}
		insert((i*53)%180-90, -i-1)
	}
	for i := range int64(20) {
		insert(0, i)
	}
	insert(-1000, 42)
	slices.SortStableFunc(want, func(a, b KeyVal) int { return a.key.compare(b.key) })

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	block, err := readDiskPage(file, 0)
	if err != nil {
		t.Fatal(err)
	}
	var meta MetaPage
	if err := meta.UnmarshalBinary(block[:]); err != nil {
		t.Fatal(err)
	}
	var got []KeyVal
	_, depth := collectDiskEntries(t, file, meta.header.nextPage, &got)
	if depth < 3 {
		t.Errorf("leaf depth = %d, want at least 3 after repeated root splits", depth)
	}
	if len(got) != len(want) {
		t.Fatalf("found %d records, want %d", len(got), len(want))
	}
	for i, entry := range want {
		if got[i] != entry {
			t.Fatalf("record %d = (%d, %d), want (%d, %d)", i,
				int64(binary.BigEndian.Uint64(got[i].key.data[:])), int64(binary.BigEndian.Uint64(got[i].value[:])),
				int64(binary.BigEndian.Uint64(entry.key.data[:])), int64(binary.BigEndian.Uint64(entry.value[:])))
		}
	}
}

func collectDiskEntries(t *testing.T, file *os.File, offset uint64, entries *[]KeyVal) (KeyEntry, int) {
	t.Helper()
	block, err := readDiskPage(file, offset)
	if err != nil {
		t.Fatal(err)
	}
	if block[0] == leafPageType {
		var leaf BTreeLeafPage
		if err := leaf.UnmarshalBinary(block[:]); err != nil || leaf.nkv == 0 || leaf.nkv >= leafMaxKV {
			t.Fatalf("invalid leaf at %d with %d records: %v", offset, leaf.nkv, err)
		}
		*entries = append(*entries, leaf.kv[:leaf.nkv]...)
		return leaf.kv[0].key, 0
	}
	var page BTreeInternalPage
	if err := page.UnmarshalBinary(block[:]); err != nil || page.nkey == 0 || page.nkey >= INTERNAL_MAX_KEY {
		t.Fatalf("invalid internal page at %d with %d children: %v", offset, page.nkey, err)
	}
	depth := 0
	for i, child := range page.children[:page.nkey] {
		if child == 0 || child >= offset {
			t.Fatalf("invalid child offset %d in page %d", child, offset)
		}
		minKey, childDepth := collectDiskEntries(t, file, child, entries)
		if page.keys[i].compare(minKey) != 0 {
			t.Fatalf("key %d in page %d does not match its child's minimum", i, offset)
		}
		if i == 0 {
			depth = childDepth + 1
		} else if depth != childDepth+1 {
			t.Fatalf("unbalanced children in page %d", offset)
		}
	}
	return page.keys[0], depth
}
