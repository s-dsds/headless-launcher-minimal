package launcher

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const MaxScriptBytes = 4 << 20

type Config struct {
	Rooms []Profile `json:"rooms"`
}

type Profile struct {
	ID        string   `json:"id"`
	Scripts   []string `json:"scripts"`
	TokenEnv  string   `json:"tokenEnv"`
	Autostart bool     `json:"autostart"`
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func LoadConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	if d.Decode(new(any)) != io.EOF {
		return c, fmt.Errorf("config must contain one JSON object")
	}
	if len(c.Rooms) == 0 || len(c.Rooms) > 64 {
		return c, fmt.Errorf("configure between 1 and 64 rooms")
	}
	seen := map[string]bool{}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return c, err
	}
	for i := range c.Rooms {
		p := &c.Rooms[i]
		if !validID.MatchString(p.ID) || seen[p.ID] {
			return c, fmt.Errorf("invalid or duplicate room id %q", p.ID)
		}
		seen[p.ID] = true
		if len(p.Scripts) == 0 {
			return c, fmt.Errorf("room %s needs scripts", p.ID)
		}
		for j, s := range p.Scripts {
			if s == "" {
				return c, fmt.Errorf("empty script path for %s", p.ID)
			}
			if !filepath.IsAbs(s) {
				p.Scripts[j] = filepath.Join(base, s)
			}
		}
		if _, err := ReadScripts(p.Scripts); err != nil {
			return c, fmt.Errorf("room %s: %w", p.ID, err)
		}
	}
	return c, nil
}

type Script struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

func ReadScripts(paths []string) ([]Script, error) {
	var scripts []Script
	total := 0
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(f, MaxScriptBytes+1))
		f.Close()
		if err != nil {
			return nil, err
		}
		total += len(b)
		if total > MaxScriptBytes {
			return nil, fmt.Errorf("scripts exceed 4 MiB")
		}
		scripts = append(scripts, Script{Name: filepath.Base(path), Source: string(b)})
	}
	return scripts, nil
}
