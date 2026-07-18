package chatstore

import (
	"encoding/json"
	"testing"
)

func TestAppendQueryPaging(t *testing.T) {
	s := New(t.TempDir())
	base := int64(1784000000000) // fixed day bucket
	for i := 0; i < 10; i++ {
		payload, _ := json.Marshal(map[string]any{
			"ts": base + int64(i)*1000, "name": "p", "auth": "a", "msg": "m",
		})
		if err := s.Append("room1", string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	days, err := s.Dates("room1")
	if err != nil || len(days) != 1 {
		t.Fatalf("dates: %v %v", days, err)
	}

	// newest-first, limit 4
	msgs, err := s.Query("room1", days[0], 4, 0)
	if err != nil || len(msgs) != 4 {
		t.Fatalf("query: %d %v", len(msgs), err)
	}
	var first Message
	json.Unmarshal(msgs[0], &first)
	if first.Ts != base+9000 {
		t.Fatalf("want newest first, got ts=%d", first.Ts)
	}

	// paging: strictly older than the last of the first page
	var last Message
	json.Unmarshal(msgs[3], &last)
	page2, _ := s.Query("room1", days[0], 4, last.Ts)
	var p2first Message
	json.Unmarshal(page2[0], &p2first)
	if p2first.Ts != last.Ts-1000 {
		t.Fatalf("paging broken: %d vs %d", p2first.Ts, last.Ts)
	}
}

func TestSearch(t *testing.T) {
	s := New(t.TempDir())
	// two days: day1 has daro, day2 (newer) has momo + daro
	day1, day2 := int64(1784000000000), int64(1784100000000)
	add := func(ts int64, name, msg string) {
		p, _ := json.Marshal(map[string]any{"ts": ts, "name": name, "auth": "a_" + name, "msg": msg})
		if err := s.Append("room1", string(p)); err != nil {
			t.Fatal(err)
		}
	}
	add(day1, "daro", "old message")
	add(day2, "momo", "hi there")
	add(day2+1000, "daro", "GG all")

	// name match, case-insensitive, newest first, across days
	msgs, _, err := s.Search("room1", "DARO", 10, "")
	if err != nil || len(msgs) != 2 {
		t.Fatalf("search daro: %d %v", len(msgs), err)
	}
	var first Message
	json.Unmarshal(msgs[0], &first)
	if first.Msg != "GG all" {
		t.Fatalf("want newest first, got %q", first.Msg)
	}
	// msg match
	if m, _, _ := s.Search("room1", "hi there", 10, ""); len(m) != 1 {
		t.Fatalf("msg search: %d", len(m))
	}
	// auth match
	if m, _, _ := s.Search("room1", "a_momo", 10, ""); len(m) != 1 {
		t.Fatalf("auth search: %d", len(m))
	}
	// empty q rejected
	if _, _, err := s.Search("room1", "", 10, ""); err == nil {
		t.Fatal("want empty q rejected")
	}
}

func TestRejects(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Append("room1", "not json"); err == nil {
		t.Fatal("want malformed payload rejected")
	}
	if err := s.Append("../evil", `{"ts":1,"msg":"x"}`); err == nil {
		t.Fatal("want bad room id rejected")
	}
	if _, err := s.Query("room1", "2026-01-01", 10, 0); err == nil {
		t.Fatal("want bad date rejected")
	}
}
