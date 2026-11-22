package main

import (
	"bytes"
	"encoding/hex"
	"slices"
	"testing"
)

func TestInternalPageInsertAndSplit(t *testing.T) {
	page := NewIPage()
	keys := []int64{3, 10, 5, -2, 0, 8, -5, 12}
	for i, key := range keys {
		page.InsertKV(NewKeyEntryFromInt(key), uint64(i+1)*4096)
	}
	if got := page.FindLastLE(NewKeyEntryFromInt(-1)); got != 1 {
		t.Errorf("FindLastLE(-1) = %d, want 1", got)
	}
	if got := page.FindLastLE(NewKeyEntryFromInt(-6)); got != -1 {
		t.Errorf("FindLastLE(-6) = %d, want -1", got)
	}

	right := page.Split()
	for _, tt := range []struct {
		page       BTreeInternalPage
		keys       []int64
		childSlots []uint64
	}{
		{page, []int64{-5, -2, 0, 3}, []uint64{7, 4, 5, 1}},
		{right, []int64{5, 8, 10, 12}, []uint64{3, 6, 2, 8}},
	} {
		if int(tt.page.nkey) != len(tt.keys) {
			t.Fatalf("key count = %d, want %d", tt.page.nkey, len(tt.keys))
		}
		for i, key := range tt.keys {
			if tt.page.keys[i] != NewKeyEntryFromInt(key) || tt.page.children[i] != tt.childSlots[i]*4096 {
				t.Errorf("slot %d does not hold key %d and offset %d", i, key, tt.childSlots[i]*4096)
			}
		}
		encoded, err := tt.page.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		var decoded BTreeInternalPage
		if err := decoded.UnmarshalBinary(encoded); err != nil || decoded != tt.page {
			t.Fatalf("page after split did not round trip: %v", err)
		}
	}
}

func TestInternalPageBinaryFormat(t *testing.T) {
	// Header, two keys (one short), then their two page offsets.
	wire, err := hex.DecodeString("010000000000002000" + "0002" +
		"0008fffffffffffffffe" + "000105" + "0000000000001000" + "0000000000003000")
	if err != nil {
		t.Fatal(err)
	}
	var decoded BTreeInternalPage
	if err := decoded.UnmarshalBinary(append(slices.Clone(wire), 0, 0)); err != nil {
		t.Fatal(err)
	}
	if decoded.header.nextPage != 8192 || decoded.nkey != 2 || decoded.keys[0] != NewKeyEntryFromInt(-2) ||
		decoded.keys[1].compare(NewKeyEntryFromInt(5)) != 0 || decoded.children[0] != 4096 || decoded.children[1] != 12288 {
		t.Fatalf("decoded unexpected page: %+v", decoded)
	}
	encoded, err := decoded.MarshalBinary()
	if err != nil || !bytes.Equal(encoded, wire) {
		t.Fatalf("encoded %x, want %x (err: %v)", encoded, wire, err)
	}

	before := decoded
	for i := range len(wire) {
		if err := decoded.UnmarshalBinary(wire[:i]); err == nil || decoded != before {
			t.Fatalf("truncation at byte %d: err = %v, page changed = %t", i, err, decoded != before)
		}
	}
	for _, mutation := range []struct {
		index int
		value byte
	}{
		{0, leafPageType},
		{pageHeaderSize + 1, INTERNAL_MAX_KEY + 1},
		{pageHeaderSize + uint16Size + 1, maxKeySize + 1},
	} {
		invalid := slices.Clone(wire)
		invalid[mutation.index] = mutation.value
		if err := decoded.UnmarshalBinary(invalid); err == nil || decoded != before {
			t.Fatalf("invalid byte at index %d: err = %v, page changed = %t", mutation.index, err, decoded != before)
		}
	}
}

func TestMetaPageBinaryFormat(t *testing.T) {
	tree := NewBPTreeDisk()
	encoded, err := tree.head.MarshalBinary()
	if err != nil || !bytes.Equal(encoded, make([]byte, pageHeaderSize)) {
		t.Fatalf("empty tree header = %x, err = %v", encoded, err)
	}
	tree.head.header.nextPage = 4096
	encoded, err = tree.head.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var decoded MetaPage
	if err := decoded.UnmarshalBinary(encoded); err != nil || decoded != tree.head {
		t.Fatalf("meta page did not round trip: %v", err)
	}
}
