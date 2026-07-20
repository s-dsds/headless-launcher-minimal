// Package gamestore is the per-room game-history store: one SQLite file per
// room (spec: _specs/game-history.md). The room script emits one @@GAME@@
// console line per finished game; wlhl parses it here into queryable rows.
// SQLite (modernc.org/sqlite — pure Go, no CGO, Windows-safe) instead of JSONL
// because history is READ for analysis (player pages, h2h, per-map stats),
// not just tailed. RTDB keeps the bounded aggregates; this file keeps the
// unbounded per-game rows — disk is where append-forever belongs.
//
// data-dir must be LOCAL disk: WAL mode is unsafe on SMB/network shares.
package gamestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	_ "modernc.org/sqlite"
)

var roomIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// GamePlayer is one participant row of a stored game.
type GamePlayer struct {
	Auth     string   `json:"auth"`
	Name     string   `json:"name"`
	Team     *int     `json:"team,omitempty"`
	Score    int      `json:"score"`
	Kills    int      `json:"kills"`
	Deaths   int      `json:"deaths"`
	Rank     *float64 `json:"rank,omitempty"` // averaged on ties (1.5 = top tie)
	Elo      *int     `json:"elo,omitempty"`  // post-game elo (full participants, N>=2)
	EloDelta *int     `json:"eloDelta,omitempty"`
	Partial  bool     `json:"partial,omitempty"` // mid-session participant
}

// Game is the emission payload / stored record.
type Game struct {
	ID         int64        `json:"id,omitempty"` // rowid, set on read
	Ts         int64        `json:"ts"`
	Map        string       `json:"map"`
	N          int          `json:"n"`
	DurationMs int64        `json:"durationMs"`
	Winner     *string      `json:"winner"`
	Partial    bool         `json:"partial"`
	Players    []GamePlayer `json:"players"`
}

// Store manages one SQLite handle per room, opened lazily.
type Store struct {
	root string
	mu   sync.Mutex
	dbs  map[string]*sql.DB
}

func New(dataDir string) *Store {
	return &Store{root: filepath.Join(dataDir, "games"), dbs: map[string]*sql.DB{}}
}

const schema = `
CREATE TABLE IF NOT EXISTS games (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  map TEXT NOT NULL DEFAULT '',
  n INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  winner_auth TEXT,
  partial INTEGER NOT NULL DEFAULT 0,
  raw TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS games_ts ON games(ts);
CREATE INDEX IF NOT EXISTS games_map ON games(map);
CREATE TABLE IF NOT EXISTS game_players (
  game_id INTEGER NOT NULL REFERENCES games(id),
  auth TEXT NOT NULL, name TEXT NOT NULL,
  team INTEGER, score INTEGER, kills INTEGER, deaths INTEGER,
  rank REAL, elo INTEGER, elo_delta INTEGER, partial INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS gp_auth_game ON game_players(auth, game_id);
CREATE INDEX IF NOT EXISTS gp_game ON game_players(game_id);
`

// db returns (opening if needed) the room's handle.
func (s *Store) db(room string) (*sql.DB, error) {
	if !roomIDRe.MatchString(room) {
		return nil, fmt.Errorf("invalid room id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.dbs[room]; ok {
		return d, nil
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, room+".db")
	// _pragma via DSN: WAL for concurrent read/write, busy_timeout so the
	// backup CLI's second connection waits instead of erroring.
	d, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	// One writer at a time keeps SQLITE_BUSY away entirely for our access
	// pattern (single capture goroutine + occasional reads).
	d.SetMaxOpenConns(1)
	if _, err := d.Exec(schema); err != nil {
		d.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	s.dbs[room] = d
	return d, nil
}

// Append parses one @@GAME@@ payload and stores it. Malformed input is an
// error for the caller to LOG AND DROP — never fatal (chatstore precedent).
func (s *Store) Append(room, payload string) error {
	var g Game
	if err := json.Unmarshal([]byte(payload), &g); err != nil {
		return fmt.Errorf("bad game json: %w", err)
	}
	if g.Ts <= 0 || len(g.Players) == 0 || len(g.Players) > 64 {
		return fmt.Errorf("bad game payload (ts=%d players=%d)", g.Ts, len(g.Players))
	}
	if len(payload) > 64<<10 {
		return fmt.Errorf("game payload too large (%dB)", len(payload))
	}
	d, err := s.db(room)
	if err != nil {
		return err
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO games (ts,map,n,duration_ms,winner_auth,partial,raw) VALUES (?,?,?,?,?,?,?)`,
		g.Ts, g.Map, g.N, g.DurationMs, g.Winner, boolToInt(g.Partial), payload)
	if err != nil {
		return err
	}
	gid, _ := res.LastInsertId()
	for _, p := range g.Players {
		if _, err := tx.Exec(`INSERT INTO game_players (game_id,auth,name,team,score,kills,deaths,rank,elo,elo_delta,partial)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			gid, p.Auth, p.Name, p.Team, p.Score, p.Kills, p.Deaths, p.Rank, p.Elo, p.EloDelta, boolToInt(p.Partial)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// QueryOpts filters a history page.
type QueryOpts struct {
	BeforeID int64  // page cursor: rows with id < BeforeID (0 = newest)
	Limit    int    // default 50, max 150 (spec: page must fit 512KB proxy cap)
	Auth     string // only games this auth played
	Map      string // only this map
	N        int    // only games with this participant count (2 = duels)
}

// Query returns games newest-first plus the next page cursor (0 = no more).
func (s *Store) Query(room string, o QueryOpts) ([]Game, int64, error) {
	d, err := s.db(room)
	if err != nil {
		return nil, 0, err
	}
	if o.Limit <= 0 || o.Limit > 150 {
		o.Limit = 50
	}
	q := `SELECT g.id, g.raw FROM games g`
	var args []interface{}
	where := ` WHERE 1=1`
	if o.Auth != "" {
		q += ` JOIN game_players gp ON gp.game_id = g.id`
		where += ` AND gp.auth = ?`
		args = append(args, o.Auth)
	}
	if o.BeforeID > 0 {
		where += ` AND g.id < ?`
		args = append(args, o.BeforeID)
	}
	if o.Map != "" {
		where += ` AND g.map = ?`
		args = append(args, o.Map)
	}
	if o.N > 0 {
		where += ` AND g.n = ?`
		args = append(args, o.N)
	}
	q += where + ` ORDER BY g.id DESC LIMIT ?`
	args = append(args, o.Limit)

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Game
	var last int64
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, 0, err
		}
		var g Game
		if json.Unmarshal([]byte(raw), &g) != nil {
			continue
		}
		g.ID = id
		out = append(out, g)
		last = id
	}
	next := int64(0)
	if len(out) == o.Limit {
		next = last
	}
	return out, next, rows.Err()
}

// PlayerSummary aggregates one auth's games (server-side, mergeable shapes).
func (s *Store) PlayerSummary(room, auth string, sinceTs int64) (map[string]interface{}, error) {
	d, err := s.db(room)
	if err != nil {
		return nil, err
	}
	row := d.QueryRow(`SELECT COUNT(*), COALESCE(SUM(gp.kills),0), COALESCE(SUM(gp.deaths),0), COALESCE(SUM(gp.score),0),
			COALESCE(SUM(CASE WHEN g.winner_auth = gp.auth THEN 1 ELSE 0 END),0),
			COALESCE(SUM(g.duration_ms),0), COALESCE(MIN(g.ts),0), COALESCE(MAX(g.ts),0)
		FROM game_players gp JOIN games g ON g.id = gp.game_id
		WHERE gp.auth = ? AND g.ts >= ?`, auth, sinceTs)
	var games, kills, deaths, score, wins int
	var durSum, first, last int64
	if err := row.Scan(&games, &kills, &deaths, &score, &wins, &durSum, &first, &last); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"auth": auth, "games": games, "kills": kills, "deaths": deaths,
		"scoreSum": score, "wins": wins, "durationMsSum": durSum,
		"firstTs": first, "lastTs": last,
	}, nil
}

// Backup writes a consistent copy of the room DB via VACUUM INTO (works on a
// live WAL database; modernc doesn't expose the C online-backup API).
func (s *Store) Backup(ctx context.Context, room, dest string) error {
	d, err := s.db(room)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("destination %s already exists", dest)
	}
	_, err = d.ExecContext(ctx, `VACUUM INTO ?`, dest)
	return err
}

// Close closes all handles (shutdown).
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.dbs {
		d.Close()
	}
	s.dbs = map[string]*sql.DB{}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
