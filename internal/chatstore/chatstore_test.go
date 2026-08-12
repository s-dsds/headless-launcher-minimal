package chatstore

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// A busy day used to be searchable only through Query's newest-500 window:
// a match older than the newest 500 messages of its day was permanently
// unreachable (day-granular paging skips past it). Search must scan the
// whole day file.
func TestSearchFindsMatchesPastQueryCap(t *testing.T) {
	s := New(t.TempDir())
	day := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	// Oldest message of the day is the only match, then 700 fillers on the
	// same day — far past Query's 500 cap.
	put := func(i int, msg string) {
		ts := day.Add(time.Duration(i) * time.Second).UnixMilli()
		payload := fmt.Sprintf(`{"ts":%d,"name":"p%d","auth":"auth%d","msg":%q}`, ts, i, i, msg)
		if err := s.Append("room1", payload); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	put(0, "the needle evidence")
	for i := 1; i <= 700; i++ {
		put(i, "filler chatter")
	}

	msgs, _, err := s.Search("room1", "needle", 100, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 match, got %d", len(msgs))
	}
	var m Message
	if err := json.Unmarshal(msgs[0], &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Msg != "the needle evidence" {
		t.Fatalf("wrong match: %q", m.Msg)
	}

	// Query keeps its newest-500 view cap (the API contract for the chat
	// pane) — the fix must not have removed it.
	dayKey := day.Format("20060102")
	view, err := s.Query("room1", dayKey, 500, 0)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(view) != 500 {
		t.Fatalf("query view: want 500, got %d", len(view))
	}
	if err := json.Unmarshal(view[0], &m); err != nil || m.Msg != "filler chatter" {
		t.Fatalf("query should be newest-first, got %q (err %v)", m.Msg, err)
	}
}
