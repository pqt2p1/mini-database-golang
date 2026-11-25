package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestKVSetGetAndDeleteAcrossPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	db := NewKV(path)
	requireKV(t, db, []byte("missing"), nil, false)
	if deleted, err := db.Del([]byte("missing")); err != nil || deleted {
		t.Fatalf("delete from missing file = %t, %v", deleted, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("reading an absent key created a file: %v", err)
	}

	for i := range 80 {
		key := byte((i * 37) % 80)
		if err := db.Set([]byte{key, 0}, []byte{0, key}); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []byte{0, 40, 79} {
		if err := db.Set([]byte{key, 0}, []byte("updated")); err != nil {
			t.Fatal(err)
		}
	}
	db = NewKV(path)
	for key := range byte(80) {
		want := []byte{0, key}
		if key == 0 || key == 40 || key == 79 {
			want = []byte("updated")
		}
		requireKV(t, db, []byte{key, 0}, want, true)
	}
	for i := range 80 {
		key := []byte{byte((i * 23) % 80), 0}
		if deleted, err := db.Del(key); err != nil || !deleted {
			t.Fatalf("delete %x = %t, %v", key, deleted, err)
		}
		requireKV(t, db, key, nil, false)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) < pageHeaderSize || binary.BigEndian.Uint64(data[1:pageHeaderSize]) != 0 {
		t.Fatalf("deleting every key did not clear the root: %v", err)
	}
	if deleted, err := db.Del([]byte{0, 0}); err != nil || deleted {
		t.Fatalf("delete from empty tree = %t, %v", deleted, err)
	}
	if err := db.Set(nil, nil); err != nil {
		t.Fatal(err)
	}
	requireKV(t, NewKV(path), nil, nil, true)
}

func TestKVPersistsDistinctByteKeysAndValidatesSizes(t *testing.T) {
	db := NewKV(filepath.Join(t.TempDir(), "kv.db"))
	for i, key := range [][]byte{nil, {0}, {0, 0}, {1}, {0, 1}, {0xff}} {
		if err := db.Set(key, []byte{0, byte(i), 0}); err != nil {
			t.Fatal(err)
		}
	}
	for i, key := range [][]byte{nil, {0}, {0, 0}, {1}, {0, 1}, {0xff}} {
		requireKV(t, db, key, []byte{0, byte(i), 0}, true)
	}
	tooLong := []byte("123456789")
	if err := db.Set(tooLong, nil); err == nil {
		t.Fatal("Set accepted a key longer than eight bytes")
	}
	if err := db.Set([]byte{1}, tooLong); err == nil {
		t.Fatal("Set accepted a value longer than eight bytes")
	}
	if _, _, err := db.Get(tooLong); err == nil {
		t.Fatal("Get accepted a key longer than eight bytes")
	}
	if _, err := db.Del(tooLong); err == nil {
		t.Fatal("Del accepted a key longer than eight bytes")
	}
	requireKV(t, db, []byte{1}, []byte{0, 3, 0}, true)
}

func TestKVOperatesOnLegacyIntegerRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	tree := NewBPTreeDisk(path)
	for i := range int64(10) {
		if err := tree.Insert(-1, i); err != nil {
			t.Fatal(err)
		}
	}
	key := binary.BigEndian.AppendUint64(nil, ^uint64(0))
	db := NewKV(path)
	requireKV(t, db, key, binary.BigEndian.AppendUint64(nil, 9), true)
	if err := db.Set(key, []byte("new")); err != nil {
		t.Fatal(err)
	}
	requireKV(t, db, key, []byte("new"), true)
	for i := 9; i >= 0; i-- {
		if deleted, err := db.Del(key); err != nil || !deleted {
			t.Fatalf("delete duplicate %d = %t, %v", i, deleted, err)
		}
		if i > 0 {
			requireKV(t, db, key, binary.BigEndian.AppendUint64(nil, uint64(i-1)), true)
		}
	}
	requireKV(t, db, key, nil, false)
}

func requireKV(t *testing.T, db *KV, key, want []byte, wantFound bool) {
	t.Helper()
	got, found, err := db.Get(key)
	if err != nil || found != wantFound || !bytes.Equal(got, want) {
		t.Fatalf("Get(%x) = %x, %t, %v, want %x, %t, nil", key, got, found, err, want, wantFound)
	}
}
