// Package chatstore persists per-room chat messages as day-bucketed JSONL
// files (<dir>/chat/<room>/<YYYYMMDD>.jsonl) — the local replacement for the
// RTDB comments node. Append-only; queried newest-first with ts-based paging,
// mirroring the panel's "Load older" UX.
package chatstore

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	mu  sync.Mutex
	dir string
}

// Message is one chat line. Extra fields in the stored JSON are preserved on
// query (we return raw JSON), this struct is only for validation/bucketing.
type Message struct {
	Ts   int64  `json:"ts"`
	Name string `json:"name"`
	Auth string `json:"auth"`
	Msg  string `json:"msg"`
}

var roomIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var dateRe = regexp.MustCompile(`^\d{8}$`)

func New(dir string) *Store { return &Store{dir: dir} }

func (s *Store) roomDir(roomID string) (string, error) {
	if !roomIDRe.MatchString(roomID) {
		return "", fmt.Errorf("invalid room id")
	}
	return filepath.Join(s.dir, "chat", roomID), nil
}

// Append validates and stores one chat payload (a JSON object). Malformed
// payloads are rejected, not stored — the raw console line is still in the
// room log, so nothing is lost.
func (s *Store) Append(roomID, payload string) error {
	var m Message
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return fmt.Errorf("chat payload: %w", err)
	}
	if m.Ts <= 0 {
		m.Ts = time.Now().UnixMilli()
	}
	// Re-marshal so the stored line is exactly one normalized JSON object per
	// line (payload could contain embedded newlines in msg — json.Marshal
	// escapes them).
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	dir, err := s.roomDir(roomID)
	if err != nil {
		return err
	}
	day := time.UnixMilli(m.Ts).Format("20060102")

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, day+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// Dates lists the days (YYYYMMDD, newest first) that have chat for the room.
func (s *Store) Dates(roomID string) ([]string, error) {
	dir, err := s.roomDir(roomID)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var days []string
	for _, e := range ents {
		name := e.Name()
		if len(name) == 14 && name[8:] == ".jsonl" && dateRe.MatchString(name[:8]) {
			days = append(days, name[:8])
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	return days, nil
}

// Search scans day files NEWEST-first for messages whose name, auth or msg
// contains q (case-insensitive), until `limit` hits or searchMaxDays files are
// scanned. beforeDay ("" = start at the newest) pages further back: pass the
// returned scannedTo as the next beforeDay. Local JSONL makes this cheap —
// the RTDB model couldn't query nested fields at all.
func (s *Store) Search(roomID, q string, limit int, beforeDay string) (msgs []json.RawMessage, scannedTo string, err error) {
	const searchMaxDays = 60
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	needle := strings.ToLower(q)
	if needle == "" {
		return nil, "", fmt.Errorf("q is required")
	}
	days, err := s.Dates(roomID) // newest first
	if err != nil {
		return nil, "", err
	}
	scanned := 0
	for _, day := range days {
		if beforeDay != "" && day >= beforeDay {
			continue
		}
		if scanned >= searchMaxDays || len(msgs) >= limit {
			break
		}
		scanned++
		scannedTo = day
		dayMsgs, err := s.Query(roomID, day, 500, 0) // newest-first within the day
		if err != nil {
			continue
		}
		for _, raw := range dayMsgs {
			var m Message
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			if strings.Contains(strings.ToLower(m.Name), needle) ||
				strings.Contains(strings.ToLower(m.Auth), needle) ||
				strings.Contains(strings.ToLower(m.Msg), needle) {
				msgs = append(msgs, raw)
				if len(msgs) >= limit {
					break
				}
			}
		}
	}
	if msgs == nil {
		msgs = []json.RawMessage{}
	}
	return msgs, scannedTo, nil
}

// Query returns up to limit messages for the day, NEWEST first, optionally
// only those strictly older than beforeTs (0 = no bound) — ts-based paging.
func (s *Store) Query(roomID, date string, limit int, beforeTs int64) ([]json.RawMessage, error) {
	if !dateRe.MatchString(date) {
		return nil, fmt.Errorf("invalid date")
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	dir, err := s.roomDir(roomID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(dir, date+".jsonl"))
	if os.IsNotExist(err) {
		return []json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Day files are bounded (one room-day of chat) — a linear read is fine.
	var kept []json.RawMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		raw := sc.Bytes()
		var m Message
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if beforeTs > 0 && m.Ts >= beforeTs {
			continue
		}
		kept = append(kept, json.RawMessage(append([]byte(nil), raw...)))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// File is append-ordered (oldest→newest): keep the newest `limit`, reversed.
	if len(kept) > limit {
		kept = kept[len(kept)-limit:]
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return kept, nil
}
