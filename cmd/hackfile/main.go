package main

import (
	"fmt"
	"os"

	"headless-launcher-go/internal/hack"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, `usage: hackfile <beautified-headless-min.js> <output.js>

Applies the wlhl hack pack (weapon-ban API, onPlayerHit/onPlayerSpawn
callbacks, player worm ref) to a captured webliero headless client script.

The input MUST be beautified first — the structural regexes assume
js-beautify spacing. Typical pipeline:
  curl -s https://www.webliero.com/v/20/headless-min.js -o raw.js
  js-beautify raw.js -o pretty.js       # npm i -g js-beautify (or python jsbeautifier)
  hackfile pretty.js hacked-min.js
Then launch the room with:  wlhl launch --id X --token T --script hacked-min.js`)
		os.Exit(2)
	}
	src, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		os.Exit(1)
	}
	res, err := hack.HackScript(string(src))
	if err != nil {
		fmt.Fprintln(os.Stderr, "hack:", err)
		os.Exit(1)
	}
	// report which weapon symbols resolved
	for _, k := range []string{"WEAPON_BAN_MSG", "WEAPON_LIST_FIELD", "WEAPON_BAN_INDEX_FIELD", "EVENT_DISPATCH"} {
		fmt.Fprintf(os.Stderr, "  %s = %q\n", k, res.Matches[k])
	}
	hasWeapons := false
	for _, f := range []string{"unbanAllWeapons", "banWeaponById", "getWeapons"} {
		if contains(res.Script, f) {
			hasWeapons = true
		}
	}
	fmt.Fprintln(os.Stderr, "weapon funcs emitted:", hasWeapons)
	os.WriteFile(os.Args[2], []byte(res.Script), 0644)
	fmt.Fprintln(os.Stderr, "wrote", len(res.Script), "bytes to", os.Args[2])
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
