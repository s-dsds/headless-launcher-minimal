package launcher

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const MaxScriptBytes = 4 << 20
const MaxStoreBytes = 64 << 20

type Config struct {
	Rooms []Profile `json:"rooms"`
}
type Settings struct {
	MaxPlayers int    `json:"maxPlayers"`
	Public     bool   `json:"public"`
	Password   string `json:"password"`
}
type Profile struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Settings          Settings `json:"settings"`
	ScriptCreatesRoom bool     `json:"scriptCreatesRoom"`
	Scripts           []Script `json:"scripts"`
}
type Script struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func ValidateScripts(scripts []Script) error {
	if len(scripts) > 100 {
		return fmt.Errorf("at most 100 scripts per room")
	}
	total := 0
	for _, s := range scripts {
		if strings.TrimSpace(s.Name) == "" || len(s.Name) > 512 {
			return fmt.Errorf("each script needs a name of at most 512 bytes")
		}
		total += len(s.Source)
	}
	if total > MaxScriptBytes {
		return fmt.Errorf("scripts exceed 4 MiB")
	}
	return nil
}
func ValidateProfile(p Profile) error {
	if !validID.MatchString(p.ID) {
		return fmt.Errorf("invalid room id")
	}
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 120 {
		return fmt.Errorf("room name must contain 1–120 bytes")
	}
	if p.Settings.MaxPlayers < 1 || p.Settings.MaxPlayers > 32 {
		return fmt.Errorf("max players must be between 1 and 32")
	}
	if len(p.Settings.Password) > 128 {
		return fmt.Errorf("password is too long")
	}
	if p.ScriptCreatesRoom && len(p.Scripts) == 0 {
		return fmt.Errorf("select scripts that create the room, or let the launcher create it")
	}
	return ValidateScripts(p.Scripts)
}
func cloneProfile(p Profile) Profile { p.Scripts = append([]Script{}, p.Scripts...); return p }

// A missing store is a normal first launch. Tokens are deliberately absent.
func LoadConfig(path string) (Config, error) {
	c := Config{Rooms: []Profile{}}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxStoreBytes+1))
	if err != nil {
		return c, err
	}
	if len(b) > MaxStoreBytes {
		return c, fmt.Errorf("room storage exceeds 64 MiB")
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("read saved rooms: %w", err)
	}
	if len(c.Rooms) > 64 {
		return c, fmt.Errorf("at most 64 saved rooms")
	}
	seen := map[string]bool{}
	for _, p := range c.Rooms {
		if err = ValidateProfile(p); err != nil {
			return c, err
		}
		if seen[p.ID] {
			return c, fmt.Errorf("duplicate room id")
		}
		seen[p.ID] = true
	}
	return c, nil
}

// Write and sync the complete snapshot before replacing the old file.
func SaveConfig(path string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > MaxStoreBytes {
		return fmt.Errorf("saved rooms exceed 64 MiB")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".rooms-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Files preserve argument order; directories expand recursively to sorted .js files.
func ReadScripts(paths []string) ([]Script, error) {
	var scripts []Script
	total := 0
	add := func(path, name string) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, MaxScriptBytes+1))
		if err != nil {
			return err
		}
		total += len(b)
		if total > MaxScriptBytes {
			return fmt.Errorf("scripts exceed 4 MiB")
		}
		scripts = append(scripts, Script{Name: filepath.ToSlash(name), Source: string(b)})
		return ValidateScripts(scripts)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if err = add(path, filepath.Base(path)); err != nil {
				return nil, err
			}
			continue
		}
		var files []string
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				if p != path && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.EqualFold(filepath.Ext(p), ".js") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
		for _, file := range files {
			name, _ := filepath.Rel(path, file)
			if err = add(file, name); err != nil {
				return nil, err
			}
		}
	}
	return scripts, nil
}
