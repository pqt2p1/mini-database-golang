package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestSchemaCRUDByCompositePrimaryKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.db")
	db := NewDB(path)
	def := TableDef{Name: "people", Cols: []string{"name", "age", "city", "score"},
		Types: []uint8{TypeBytes, TypeInt64, TypeBytes, TypeInt64}, PKeys: 2}
	if err := db.kv.Set([]byte("legacy"), []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTable(def); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTable(def); err == nil {
		t.Fatal("duplicate table created")
	}
	name := []byte{'A', 0, 255}
	key := func() *Record { return (&Record{}).AddInt64("age", -30).AddStr("name", name) }
	row := func(city string, score int64) Record {
		return *key().AddInt64("score", score).AddStr("city", []byte(city))
	}
	if updated, err := db.Update("people", row("Hanoi", 10)); err != nil || updated {
		t.Fatalf("update absent row = %t, %v", updated, err)
	}
	if inserted, err := db.Insert("people", row("Hanoi", 10)); err != nil || !inserted {
		t.Fatalf("insert row = %t, %v", inserted, err)
	}
	if inserted, err := db.Insert("people", row("unused", 20)); err != nil || inserted {
		t.Fatalf("duplicate insert = %t, %v", inserted, err)
	}
	if updated, err := db.Update("people", row("Hue", math.MinInt64)); err != nil || !updated {
		t.Fatalf("update row = %t, %v", updated, err)
	}

	db = NewDB(path)
	stored, err := db.Table("people")
	if err != nil || stored.Prefix == 0 {
		t.Fatalf("reopen table: %+v, %v", stored, err)
	}
	def.Prefix = stored.Prefix
	if !reflect.DeepEqual(stored, def) {
		t.Fatalf("definition = %+v, want %+v", stored, def)
	}
	got := key()
	found, err := db.Get("people", got)
	want := (&Record{}).AddStr("name", name).AddInt64("age", -30).AddStr("city", []byte("Hue")).AddInt64("score", math.MinInt64)
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("Get = %+v, %t, %v, want %+v", got, found, err, want)
	}
	def.Name, def.Prefix = "visitors", 0
	if err := db.CreateTable(def); err != nil {
		t.Fatal(err)
	}
	if err := db.Upsert("visitors", row("Paris", 5)); err != nil {
		t.Fatal(err)
	}
	if deleted, err := db.Delete("people", *key()); err != nil || !deleted {
		t.Fatalf("delete row = %t, %v", deleted, err)
	}
	if found, err := db.Get("people", key()); err != nil || found {
		t.Fatalf("get deleted row = %t, %v", found, err)
	}
	if deleted, err := db.Delete("people", *key()); err != nil || deleted {
		t.Fatalf("delete absent row = %t, %v", deleted, err)
	}
	if err := db.Upsert("visitors", row("Rome", 6)); err != nil {
		t.Fatal(err)
	}
	got = key()
	if found, err := db.Get("visitors", got); err != nil || !found || string(got.Vals[2].Str) != "Rome" {
		t.Fatalf("get other table = %+v, %t, %v", got, found, err)
	}
	requireKV(t, db.kv, []byte("legacy"), []byte("old"), true)
}

func TestSchemaRejectsInvalidRows(t *testing.T) {
	db := NewDB(filepath.Join(t.TempDir(), "schema.db"))
	def := TableDef{Name: "items", Cols: []string{"id", "label"}, Types: []uint8{TypeBytes, TypeBytes}, PKeys: 1}
	if err := db.CreateTable(def); err != nil {
		t.Fatal(err)
	}
	for _, record := range []*Record{
		(&Record{}).AddStr("label", []byte("missing id")),
		(&Record{}).AddInt64("id", 1).AddStr("label", nil),
		(&Record{}).AddStr("id", nil).AddStr("id", []byte("duplicate")),
		(&Record{}).AddStr("id", bytes.Repeat([]byte{0}, 8)).AddStr("label", nil),
		(&Record{}).AddStr("id", nil).AddStr("label", make([]byte, maxValueSize)),
	} {
		if inserted, err := db.Insert("items", *record); err == nil || inserted {
			t.Fatalf("accepted invalid record %+v: %t, %v", record, inserted, err)
		}
	}
	if found, err := db.Get("items", (&Record{}).AddStr("id", nil)); err != nil || found {
		t.Fatalf("unexpected row after failed inserts: %t, %v", found, err)
	}
	if err := db.CreateTable(TableDef{Name: "bad", Cols: []string{"a", "a"}, Types: def.Types, PKeys: 1}); err == nil {
		t.Fatal("accepted repeated column names")
	}
}

func TestRecordKeyOrdersSignedIntegers(t *testing.T) {
	var previous [maxKeySize]byte
	for _, value := range []int64{math.MinInt64, -65537, -65536, -257, -256, -1, 0, 1, 255, 256, math.MaxInt64} {
		key, err := encodeRecordKey(1, []Value{{Type: TypeInt64, I64: value}})
		if err != nil || bytes.Compare(previous[:], key[:]) >= 0 {
			t.Fatalf("key for %d = %x, not after %x (err: %v)", value, key, previous, err)
		}
		previous = key
	}
}

func TestLongKeysSplitPagesByEncodedSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wide.db")
	db := NewKV(path)
	value := bytes.Repeat([]byte{255}, maxValueSize)
	keys := func(i int) []byte { return binary.BigEndian.AppendUint64(make([]byte, 8), uint64(i)) }
	for i := range leafMaxKV - 1 {
		if err := db.Set(keys(i), value); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := readDiskEntries(t, path)
	if len(got) != leafMaxKV-1 || !slices.IsSortedFunc(got, func(a, b KeyVal) int { return a.key.compare(b.key) }) {
		t.Fatalf("got %d sorted records, expected %d", len(got), leafMaxKV-1)
	}
	db = NewKV(path)
	for i := range leafMaxKV - 1 {
		requireKV(t, db, keys(i), value, true)
	}
}
