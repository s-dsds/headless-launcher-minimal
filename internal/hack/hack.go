package hack

import (
	"fmt"
	"regexp"
	"strings"
)

const eventCallbacksPlaceholder = "#EVENT_CALLBACKS_PLACEHOLDER#"

// Result holds the output of hackScript.
type Result struct {
	Script           string
	Paths            map[string]string
	Matches          map[string]string
	ExtendedApiFuncs []string
	EventCallbacks   map[string]string
}

// GetPaths returns a copy of the current hack paths (called by __getInterestingPaths).
func GetPaths() map[string]string {
	cp := make(map[string]string, len(lastResult.Paths))
	for k, v := range lastResult.Paths {
		cp[k] = v
	}
	return cp
}

var lastResult Result

func addMatch(r *Result, k, v string) {
	fmt.Println("MATCH:", k, v)
	r.Matches[k] = v
}

func addPath(r *Result, k, v string) {
	fmt.Println("PATH:", k, v)
	r.Paths[k] = v
}

// HackScript applies regex-based transforms to expose game internals.
func HackScript(script string) (*Result, error) {
	r := &Result{
		Script:           script,
		Paths:            make(map[string]string),
		Matches:          make(map[string]string),
		ExtendedApiFuncs: nil,
		EventCallbacks:   make(map[string]string),
	}

	steps := []struct {
		name string
		fn   func(*Result) error
	}{
		{"addHackSuccess", addHackSuccess},
		{"addRoomObjectReference", addRoomObjectReference},
		{"addReadPngRef", addReadPngRef},
		{"extendGametickAndAddPlaceholder", extendGametickAndAddPlaceholder},
		{"resolveOnWObjectFromStartFramePathMatches", resolveOnWObjectFromStartFramePathMatches},
		{"extendPlayerData", extendPlayerData},
		{"addAPIWeaponBans", addAPIWeaponBans},
		{"addOnPlayerHit", addOnPlayerHit},
		{"insertExtendedAPI", insertExtendedAPI},
		{"insertNewEventCallbacks", insertNewEventCallbacks},
	}

	for _, s := range steps {
		if err := s.fn(r); err != nil {
			return nil, fmt.Errorf("%s: %w", s.name, err)
		}
	}

	lastResult = *r
	return r, nil
}

// addHackSuccess injects window.EVIL=true at the start.
func addHackSuccess(r *Result) error {
	re := regexp.MustCompile(`(strict';\n\(function\(\w{2}\)\s{\n)`)
	old := r.Script
	r.Script = re.ReplaceAllString(r.Script, "${1}    window.EVIL=true;\n")
	if r.Script == old {
		return fmt.Errorf("hack success failed")
	}
	return nil
}

// addRoomObjectReference injects window.REF_ROOM_STATE reference.
func addRoomObjectReference(r *Result) error {
	re := regexp.MustCompile(`\w\("Invalid country code"\);\s+let\s(\w)\s=\snew\s(\w{2});\n\s+\w\.(\w)`)
	found := re.FindStringSubmatch(r.Script)
	if len(found) != 4 {
		return fmt.Errorf("addRoomObjectReference hack failed")
	}
	addMatch(r, "API_VAR", found[1])
	addMatch(r, "ROOM_CLASS", found[2])
	addPath(r, "GAME_STATE", found[3])

	re2 := regexp.MustCompile(`(\w\("Invalid country code"\);\s+let\s)(\w)(\s=\snew\s\w{2};)`)
	old := r.Script
	r.Script = re2.ReplaceAllString(r.Script, "${1}${2}${3}\n            window.REF_ROOM_STATE=${2};\n")
	if r.Script == old {
		return fmt.Errorf("ref room state failed")
	}
	return nil
}

// addReadPngRef injects window.__ReadPNG reference.
func addReadPngRef(r *Result) error {
	re := regexp.MustCompile(`(\w{2})\.\w\s=\s!0;\s+class\s\w\s{\s+static\s\w{2}\(\w\)`)
	found := re.FindStringSubmatch(r.Script)
	if len(found) != 2 {
		return fmt.Errorf("addReadPngRef hack failed")
	}
	addMatch(r, "READER", found[1])

	re2 := regexp.MustCompile(`((\w{2})\.\w\s=\s!0;)(\s+class\s\w\s{\s+static\s\w{2}\(\w\))`)
	old := r.Script
	r.Script = re2.ReplaceAllString(r.Script, "${1}\n  window.__ReadPNG = ${2}.read;\n${3}")
	if r.Script == old {
		return fmt.Errorf("ref read png failed")
	}
	return nil
}

// extendGametickAndAddPlaceholder modifies onGameTick to pass game state and adds placeholder.
func extendGametickAndAddPlaceholder(r *Result) error {
	re := regexp.MustCompile(`(\w{1})\.(\w{2})\s=\sfunction\(\)\s{\n\s+let\s(\w)\s=\s(\w)\.onGameTick;\n`)
	found := re.FindStringSubmatch(r.Script)
	if len(found) != 5 {
		return fmt.Errorf("gametick extension hack failed")
	}
	infunc := found[3]
	apiobject := found[4]
	addMatch(r, "API_OBJ", apiobject)

	// First replace: pass `this` to the infunc call
	c := regexp.MustCompile(`(\}\n\s+null\s!=\sthis\.` + regexp.QuoteMeta(infunc) + `\s&&\sthis\.` + regexp.QuoteMeta(infunc) + `\()(\)\n\s+\})`)
	old := r.Script
	r.Script = c.ReplaceAllString(r.Script, "${1}this${2}")

	// Second replace: add game param to function signature and call, plus placeholder
	re2 := regexp.MustCompile(`(\w{1}\.\w{2}\s=\sfunction\()(\)\s{\n\s+let\s\w\s=\s\w\.onGameTick;\n\s+null\s!=\s\w\s&&\s\w\()(\)\n\s+};\n)`)
	r.Script = re2.ReplaceAllString(r.Script, "${1}game${2}game${3}\n"+eventCallbacksPlaceholder)
	if r.Script == old {
		return fmt.Errorf("gametick extension failed")
	}
	return nil
}

// extendPlayerData adds worm reference to player data.
func extendPlayerData(r *Result) error {
	re := regexp.MustCompile(`(function\s\w\((\w)\)\s{\n\s+return\snull\s==\s\w\s\?\snull\s:\s{\n)`)
	old := r.Script
	r.Script = re.ReplaceAllString(r.Script, "${1}worm:${2},\n")
	if r.Script == old {
		return fmt.Errorf("player data extension failed")
	}
	return nil
}

// weaponBanClassRe anchors on the weapon-ban sync message class body. It is
// distinctive (constructor/apply/l/B shape manipulating a Map via clear/
// delete/set, gated on `this.<idx> >= <list>.length`) even though the class
// name and its two field names (list-length field, ban-index field) are
// re-minified across webliero releases. Captures: (1) class name,
// (2) weapon-list field name on `<GAME_STATE>.m`, (3) ban-index field name.
// Verified stable in structure (only the three identifiers change) across
// headless-min captures from 2022-01 through 2023-04 and the live 2026-07
// script fetched from webliero.com; the identifiers themselves flipped once
// in that window (Ta/K/vd -> Va/L/xd around 2023-03), confirming per-fetch
// resolution (not hardcoding) is required.
var weaponBanClassRe = regexp.MustCompile(
	`class\s+(\w+)\s+extends\s+p\s*\{\s*` +
		`constructor\(\)\s*\{\s*super\(\)\s*\}\s*` +
		`apply\(a\)\s*\{\s*if\s*\(2\s*==\s*\(a\.eb\(this\.\w+\)\s*&\s*2\)\)\s*\{\s*` +
		`var\s+b\s*=\s*a\.D\.m\.(\w+)\.length;\s*` +
		`if\s*\(!\(this\.(\w+)\s*>=\s*b\)\)\s*` +
		`if\s*\(0\s*>\s*this\.\w+\)\s*` +
		`if\s*\(0\s*==\s*this\.newValue\)\s*a\.jb\.clear\(\);\s*` +
		`else\s*\{\s*if\s*\(1\s*==\s*this\.newValue\)\s*\{\s*` +
		`let\s+c\s*=\s*0;\s*for\s*\(;\s*c\s*<\s*b;\)\s*a\.jb\.set\(c\+\+,\s*1\)\s*\}\s*\}\s*` +
		`else\s+0\s*==\s*this\.newValue\s*\?\s*a\.jb\.delete\(this\.\w+\)\s*:\s*a\.jb\.set\(this\.\w+,\s*` +
		`this\.newValue\)\s*\}\s*\}\s*` +
		`l\(a\)\s*\{\s*a\.g\(this\.\w+\);\s*a\.f\(this\.newValue\)\s*\}\s*` +
		`B\(a\)\s*\{\s*this\.\w+\s*=\s*a\.h\(\);\s*this\.newValue\s*=\s*a\.o\(\)\s*\}\s*` +
		`\}`,
)

// weaponBanDispatchRe anchors on the local message-send helper: a named,
// single-param function whose entire body defers to a resolved Promise then
// calls a single method on the connection object with that same param. This
// shape is unique in the captured scripts (the only other `R.then(...)`
// call sites are inline ternaries inside sendChat/sendAnnouncement with a
// 2-arg call, not a standalone named function declaration). Captures:
// (1) the dispatch function name.
var weaponBanDispatchRe = regexp.MustCompile(
	`function\s+(\w+)\(\w+\)\s*\{\s*` +
		`\w+\.then\(function\(\)\s*\{\s*` +
		`\w+\.\w+\(\w+\)\s*\}\)\s*\}`,
)

// addAPIWeaponBans resolves the weapon-ban message class and the local
// dispatch fn, then emits unbanAllWeapons/banWeapon/banWeaponById/
// getWeapon/getWeapons into r.ExtendedApiFuncs, ported from
// dock/webliero-extended-scripts/headless-extended.js:6693-6747.
//
// Resolution failure is logged and non-fatal: a drifted anchor must not
// abort HackScript (which would brick room hosting entirely), it just means
// the weapon-ban funcs are not emitted this run.
func addAPIWeaponBans(r *Result) error {
	classMatches := weaponBanClassRe.FindAllStringSubmatch(r.Script, -1)
	if len(classMatches) != 1 {
		fmt.Printf("WARN: addAPIWeaponBans: weapon-ban class anchor matched %d times (want 1), skipping weapon-ban API\n", len(classMatches))
		return nil
	}
	dispatchMatches := weaponBanDispatchRe.FindAllStringSubmatch(r.Script, -1)
	if len(dispatchMatches) != 1 {
		fmt.Printf("WARN: addAPIWeaponBans: dispatch fn anchor matched %d times (want 1), skipping weapon-ban API\n", len(dispatchMatches))
		return nil
	}

	banClass := classMatches[0][1]
	weaponListField := classMatches[0][2]
	banIndexField := classMatches[0][3]
	dispatchFn := dispatchMatches[0][1]

	addMatch(r, "WEAPON_BAN_MSG", banClass)
	addMatch(r, "WEAPON_LIST_FIELD", weaponListField)
	addMatch(r, "WEAPON_BAN_INDEX_FIELD", banIndexField)
	addMatch(r, "EVENT_DISPATCH", dispatchFn)

	apiVar := r.Matches["API_VAR"]
	gameState := r.Paths["GAME_STATE"]
	weaponList := fmt.Sprintf("%s.%s.m.%s", apiVar, gameState, weaponListField)

	funcs := []string{
		fmt.Sprintf(`unbanAllWeapons: function() {
                    let c = new %s;
                    c.%s = -1;
                    c.newValue = 0;
                    %s(c);
                }`, banClass, banIndexField, dispatchFn),
		fmt.Sprintf(`banWeapon: function(name, ban) {
                    var W = %s;
                    for (let i = 0; i < W.length; i++) {
                        if (W[i].name == name) {
                            let c = new %s;
                            c.%s = i;
                            c.newValue = ban ? 1 : 0;
                            %s(c);
                            break;
                        }
                    }
                }`, weaponList, banClass, banIndexField, dispatchFn),
		fmt.Sprintf(`banWeaponById: function(id, ban) {
                    let c = new %s;
                    c.%s = id;
                    c.newValue = ban ? 1 : 0;
                    %s(c);
                }`, banClass, banIndexField, dispatchFn),
		fmt.Sprintf(`getWeapon: function(id) {
                    return %s[id];
                }`, weaponList),
		fmt.Sprintf(`getWeapons: function() {
                    return %s;
                }`, weaponList),
	}
	r.ExtendedApiFuncs = append(r.ExtendedApiFuncs, funcs...)
	return nil
}

// --- onPlayerHit emission (room-stats Phase 2) ---
//
// Vanilla headless-min.js has NO onPlayerHit callback; the damage logic exists
// but never reports the hit. We wire it in with THREE structural injections
// (spec: room-stats.md "Phase 2 hack.go"), each resolving minified identifiers
// at fetch time and each non-fatal on drift (a missed anchor just means no
// weapon stats this run — it never aborts hosting):
//
//  1. In the worm damage method `qb(a,b,c,d)` — after health `this.<h>` is
//     decremented by `b*a.<mult>` — call `a.onPlayerHit(this.C, c, dmg, d)`
//     with dmg = min(health, b*mult). `a` is the game state, `c` the shooter
//     id, `d` the weapon id, `this.C` the hurt worm id.
//  2. A game-state bridge `this.<D>.onPlayerHit = fn(hurtId,shooterId,dmg,wpn)`
//     that resolves ids to worms via the room's worm map and forwards to the
//     room object's Wph forwarder — modeled exactly on the kill bridge
//     `this.<D>.Fi = fn(b,c,d){ b=a.<F>.get(b); ... }`.
//  3. An API forwarder `<t>.Wph = fn(m,u,dmg,wpn){ let z=<G>.onPlayerHit;
//     null!=z && z(<g>(m),<g>(u),dmg,wpn) }` emitted at the event-callback
//     placeholder, modeled on the `onPlayerKilled` forwarder (symbols t/G/g).
//
// Wph is a distinct name (not "onPlayerHit") so the room object's forwarder
// can't be confused with the game-state bridge property of the same purpose.

// injection 1 anchor: the health-decrement head of the worm damage method.
// Captures (1) health field, (2) damage-multiplier field on the game state.
var hitDamageMethodRe = regexp.MustCompile(
	`(qb\(a, b, c, d\) \{\s+if \(0 < this\.(\w+)\) \{\s+let e = this\.\w+ - b \* a\.(\w+);)`)

// injection 2 anchor: the kill bridge, whose shape we mirror for the hit
// bridge. Captures (1) whole block, (2) game-state field, (3) closure var,
// (4) worm-map field.
var killBridgeRe = regexp.MustCompile(
	`(this\.(\w)\.\w{2} = function\(b, c, d\) \{\s+b = (\w)\.(\w)\.get\(b\);\s+null != b && \(c = \w\.\w\.get\(c\), \w\.\w+\.\w+\(\w, b, c\), \w+\.\w+\(\w\.\w{2}, b, c, d\)\)\s*\})`)

// injection 3 anchor: the onPlayerKilled forwarder. Captures (1) api object,
// (2) app-callbacks object, (3) player-wrapper fn.
var killForwarderRe = regexp.MustCompile(
	`(\w)\.\w{2} = function\(m, u\) \{\s+let z = (\w)\.onPlayerKilled;\s+null != z && z\((\w)\(m\), \w\(u\)\)`)

// onPlayerSpawn call-site anchor: the worm-create/spawn method builds the worm
// (`d.wm(this,c); d.color=a; d.C=b; this.<list>.push(d)`) and only assigns the
// worm id (`d.C`) AFTER wm runs — so the call must go here, after the push,
// passing the fully-built worm object `d`. `this` is the game state (the same
// instance the onPlayerSpawn bridge is attached to). Captures (1) the block up
// to the push, (2) the worm var.
var spawnMethodRe = regexp.MustCompile(
	`(\.wm\(this, \w\);\s+(\w)\.color = \w;\s+\w\.C = \w;\s+this\.\w+\.push\(\w\);)`)

func addOnPlayerHit(r *Result) error {
	dm := hitDamageMethodRe.FindAllStringSubmatch(r.Script, -1)
	if len(dm) != 1 {
		fmt.Printf("WARN: addOnPlayerHit: damage-method anchor matched %d times (want 1), skipping weapon/damage stats\n", len(dm))
		return nil
	}
	kb := killBridgeRe.FindAllStringSubmatch(r.Script, -1)
	if len(kb) != 1 {
		fmt.Printf("WARN: addOnPlayerHit: kill-bridge anchor matched %d times (want 1), skipping weapon/damage stats\n", len(kb))
		return nil
	}
	kf := killForwarderRe.FindAllStringSubmatch(r.Script, -1)
	if len(kf) != 1 {
		fmt.Printf("WARN: addOnPlayerHit: kill-forwarder anchor matched %d times (want 1), skipping weapon/damage stats\n", len(kf))
		return nil
	}

	healthField := dm[0][2]
	multField := dm[0][3]
	gameStateField := kb[0][2]
	closureVar := kb[0][3]
	wormMapField := kb[0][4]
	apiObj := kf[0][1]
	appCb := kf[0][2]
	wrapFn := kf[0][3]

	addMatch(r, "HIT_HEALTH_FIELD", healthField)
	addMatch(r, "HIT_MULT_FIELD", multField)
	addMatch(r, "HIT_WORMMAP_FIELD", wormMapField)

	// Injection 1: emit the hit call inside qb, right after the health-decrement
	// head. dmg = min(current health, raw damage); guard on the callback + dmg>0.
	call := fmt.Sprintf("${1} { let _hd = Math.min(this.%s, b * a.%s); if (_hd > 0 && null != a.onPlayerHit) a.onPlayerHit(this.C, c, _hd, d); }",
		healthField, multField)
	old := r.Script
	r.Script = hitDamageMethodRe.ReplaceAllString(r.Script, call)
	if r.Script == old {
		fmt.Printf("WARN: addOnPlayerHit: injection 1 (damage method) did not apply, skipping\n")
		return nil
	}

	// Injection 2: append the game-state bridges (hit + spawn) after the kill
	// bridge. Spawn mirrors hit but carries a single worm id.
	bridge := fmt.Sprintf(`${1}
            this.%s.onPlayerHit = function(b, c, d, e) {
                b = %s.%s.get(b);
                null != b && (c = %s.%s.get(c), null != c && null != %s.Wph && %s.Wph(b, c, d, e))
            }
            this.%s.onPlayerSpawn = function(b) {
                b = %s.%s.get(b);
                null != b && null != %s.Wsp && %s.Wsp(b)
            }`, gameStateField, closureVar, wormMapField, closureVar, wormMapField, closureVar, closureVar,
		gameStateField, closureVar, wormMapField, closureVar, closureVar)
	old = r.Script
	r.Script = killBridgeRe.ReplaceAllString(r.Script, bridge)
	if r.Script == old {
		fmt.Printf("WARN: addOnPlayerHit: injection 2 (game-state bridge) did not apply, skipping\n")
		return nil
	}

	// Injection 3: the API forwarders, emitted at the event-callback placeholder.
	r.EventCallbacks["onPlayerHit"] = fmt.Sprintf(
		`%s.Wph = function(m, u, damage, weaponID) { let z = %s.onPlayerHit; null != z && z(%s(m), %s(u), damage, weaponID) }`,
		apiObj, appCb, wrapFn, wrapFn)
	r.EventCallbacks["onPlayerSpawn"] = fmt.Sprintf(
		`%s.Wsp = function(m) { let z = %s.onPlayerSpawn; null != z && z(%s(m)) }`,
		apiObj, appCb, wrapFn)

	// Injection 4: emit the spawn call at the tail of the respawn method.
	sm := spawnMethodRe.FindAllStringSubmatch(r.Script, -1)
	if len(sm) != 1 {
		fmt.Printf("WARN: addOnPlayerHit: spawn-method anchor matched %d times (want 1), skipping spawn events\n", len(sm))
		return nil
	}
	wormVar := sm[0][2]
	spawnCall := fmt.Sprintf("${1} if (null != this.onPlayerSpawn) this.onPlayerSpawn(%s.C);", wormVar)
	old = r.Script
	r.Script = spawnMethodRe.ReplaceAllString(r.Script, spawnCall)
	if r.Script == old {
		fmt.Printf("WARN: addOnPlayerHit: spawn call-site injection did not apply, skipping spawn events\n")
	}

	return nil
}

// resolveOnWObjectFromStartFramePathMatches finds object list class and start frame paths.
func resolveOnWObjectFromStartFramePathMatches(r *Result) error {
	// Find OBJ_LIST_CLASS
	re1 := regexp.MustCompile(`(?s)class\s+(\w{2})\s+\{\n\s+constructor\(\w\,\s\w\)\s+\{\n\s+var\s\w\s=\s\[\];\n.+?\n\s+this\.list\s=\s\w;\n\s+this\.(\w)\s=\s\d\n\s+\}`)
	found1 := re1.FindStringSubmatch(r.Script)
	if len(found1) != 3 {
		return fmt.Errorf("failed on listAndObjCount")
	}
	objListClass := found1[1]
	addMatch(r, "OBJ_LIST_CLASS", objListClass)
	addPath(r, "OBJ_COUNT", found1[2])

	// Find OBJ_LIST
	re2pat := `this\.(\w{2})\s=\snew\s` + regexp.QuoteMeta(objListClass) + `\(1E3\,\sfunction\(\)\s\{\n\s+return\snew\s\w{2}\n\s+\}\);\n\s+this\.\w{2}\s=\s\[\];`
	re2 := regexp.MustCompile(re2pat)
	found2 := re2.FindStringSubmatch(r.Script)
	if len(found2) != 2 {
		return fmt.Errorf("failed on OBJ_LIST")
	}
	addPath(r, "OBJ_LIST", found2[1])

	// Find WOBJ_STARTFRAME
	re3 := regexp.MustCompile(`\w\.(\w{2})\s=\s\w\.startFrame;\n(\s+\w\.\w{2}\s=\s\w\.\w*;\n)+\s+\w\.repeat\s=\s\w\.repeat`)
	found3 := re3.FindStringSubmatch(r.Script)
	if len(found3) < 2 {
		return fmt.Errorf("failed on startFrame")
	}
	addPath(r, "WOBJ_STARTFRAME", found3[1])
	return nil
}

// insertExtendedAPI inserts extended API functions after getTeamScore.
func insertExtendedAPI(r *Result) error {
	funcs := strings.Join(r.ExtendedApiFuncs, ",\n")
	re := regexp.MustCompile(`(getTeamScore:\sfunction\(\w\)\s{\n.*\n.*})`)
	old := r.Script
	if len(funcs) > 0 {
		r.Script = re.ReplaceAllString(r.Script, "${1},\n"+funcs)
	} else {
		// Even with no extended funcs, we must verify the pattern exists
		if !re.MatchString(r.Script) {
			return fmt.Errorf("inserting extended api failed")
		}
	}
	// If we had funcs and nothing changed, that's an error
	if len(funcs) > 0 && r.Script == old {
		return fmt.Errorf("inserting extended api failed")
	}
	return nil
}

// insertNewEventCallbacks replaces the placeholder with event callback functions.
func insertNewEventCallbacks(r *Result) error {
	vals := make([]string, 0, len(r.EventCallbacks))
	for _, v := range r.EventCallbacks {
		vals = append(vals, v)
	}
	funcs := strings.Join(vals, ";\n")
	replacement := "\n/*---custom  callbacks-----*/\n" + funcs + ";\n/*------------------*/\n"
	old := r.Script
	r.Script = strings.Replace(r.Script, eventCallbacksPlaceholder, replacement, 1)
	if r.Script == old {
		return fmt.Errorf("inserting extended api callbacks failed")
	}
	return nil
}
