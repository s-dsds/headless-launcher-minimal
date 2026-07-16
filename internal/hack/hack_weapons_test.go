package hack

import (
	"os"
	"strings"
	"testing"
)

// capturedScriptPath points at the newest captured headless-min original
// (already beautified by the same js-beautify config handleFetchScript
// applies before calling HackScript). The capture lives in the sibling
// headless-launcher dir, not headless-launcher-go, so it's opened by path
// rather than embedded.
const capturedScriptPath = "/home/qmdev/liero/dock/headless-launcher/headless-min-original-1680395495.js"

// readFixture loads a captured client script, skipping the test when the
// fixture isn't present (CI: the minified webliero client is deliberately not
// committed — these tests only run on a dev box that has the captures).
func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("captured script not available (%v) — skipping (dev-box-only fixture)", err)
	}
	return raw
}

func TestAddAPIWeaponBans_ResolvesAndEmitsOnCapturedScript(t *testing.T) {
	raw := readFixture(t, capturedScriptPath)

	result, err := HackScript(string(raw))
	if err != nil {
		t.Fatalf("HackScript: %v", err)
	}

	wantMatches := map[string]string{
		"WEAPON_BAN_MSG":         "Va",
		"WEAPON_LIST_FIELD":      "L",
		"WEAPON_BAN_INDEX_FIELD": "xd",
		"EVENT_DISPATCH":         "e",
	}
	for k, want := range wantMatches {
		got, ok := result.Matches[k]
		if !ok {
			t.Fatalf("expected r.Matches[%q] to be set", k)
		}
		if got != want {
			t.Fatalf("r.Matches[%q] = %q, want %q", k, got, want)
		}
	}

	// Anchor must have matched exactly once in the captured script.
	classMatches := weaponBanClassRe.FindAllStringSubmatch(string(raw), -1)
	if len(classMatches) != 1 {
		t.Fatalf("weaponBanClassRe matched %d times in captured script, want exactly 1", len(classMatches))
	}
	dispatchMatches := weaponBanDispatchRe.FindAllStringSubmatch(string(raw), -1)
	if len(dispatchMatches) != 1 {
		t.Fatalf("weaponBanDispatchRe matched %d times in captured script, want exactly 1", len(dispatchMatches))
	}

	wantFuncNames := []string{"unbanAllWeapons", "banWeapon", "banWeaponById", "getWeapon", "getWeapons"}
	if len(result.ExtendedApiFuncs) != len(wantFuncNames) {
		t.Fatalf("len(ExtendedApiFuncs) = %d, want %d; funcs: %v", len(result.ExtendedApiFuncs), len(wantFuncNames), result.ExtendedApiFuncs)
	}
	joined := strings.Join(result.ExtendedApiFuncs, "\n")
	for _, name := range wantFuncNames {
		if !strings.Contains(joined, name+":") {
			t.Errorf("emitted funcs missing %q", name)
		}
	}

	// The emitted JS must reference the resolved literals, not placeholders.
	for _, lit := range []string{"new Va", "c.xd", "e(c)", "t.D.m.L"} {
		if !strings.Contains(joined, lit) {
			t.Errorf("emitted funcs missing expected literal %q\n---\n%s", lit, joined)
		}
	}

	// The patched script itself must contain the emitted funcs (inserted by
	// insertExtendedAPI after getTeamScore).
	if !strings.Contains(result.Script, "unbanAllWeapons: function()") {
		t.Errorf("patched script does not contain unbanAllWeapons")
	}
	if !strings.Contains(result.Script, "getWeapons: function()") {
		t.Errorf("patched script does not contain getWeapons")
	}
}

func TestWeaponBanAnchors_NoFalseMatchOnUnrelatedClasses(t *testing.T) {
	raw := readFixture(t, capturedScriptPath)
	s := string(raw)

	// Ta (a different, unrelated message class in this capture) must not be
	// picked up by the weapon-ban class anchor.
	if strings.Contains(s, "class Ta extends p") {
		classMatches := weaponBanClassRe.FindAllStringSubmatch(s, -1)
		for _, m := range classMatches {
			if m[1] == "Ta" {
				t.Fatalf("weaponBanClassRe incorrectly matched unrelated class Ta")
			}
		}
	}
}

// currentScriptPath is the live 2026-07 vanilla headless script (beautified),
// the target the onPlayerHit injections were resolved against. The 2023
// capture predates the onPlayerKilled forwarder shape, so onPlayerHit is
// tested here.
const currentScriptPath = "/home/qmdev/liero/dock/headless-launcher/headless-min-original-2026-07-current.js"

func TestAddOnPlayerHit_ThreeInjectionsOnCurrentScript(t *testing.T) {
	raw, err := os.ReadFile(currentScriptPath)
	if err != nil {
		t.Skipf("current script capture not present: %v", err)
	}
	result, err := HackScript(string(raw))
	if err != nil {
		t.Fatalf("HackScript: %v", err)
	}

	// Anchors resolve exactly once.
	if n := len(hitDamageMethodRe.FindAllStringSubmatch(string(raw), -1)); n != 1 {
		t.Fatalf("hitDamageMethodRe matched %d times, want 1", n)
	}
	if n := len(killBridgeRe.FindAllStringSubmatch(string(raw), -1)); n != 1 {
		t.Fatalf("killBridgeRe matched %d times, want 1", n)
	}
	if n := len(killForwarderRe.FindAllStringSubmatch(string(raw), -1)); n != 1 {
		t.Fatalf("killForwarderRe matched %d times, want 1", n)
	}

	// Resolved symbols.
	for k, want := range map[string]string{
		"HIT_HEALTH_FIELD":  "xa",
		"HIT_MULT_FIELD":    "we",
		"HIT_WORMMAP_FIELD": "F",
	} {
		if got := result.Matches[k]; got != want {
			t.Fatalf("Matches[%q] = %q, want %q", k, got, want)
		}
	}

	// Each injection lands exactly once in the emitted script.
	checks := map[string]int{
		"a.onPlayerHit(this.C, c, _hd, d)": 1, // injection 1 call
		"let _hd = Math.min(this.xa":       1, // injection 1 dmg calc
		"this.D.onPlayerHit = function(b, c, d, e)": 1, // injection 2 bridge
		".Wph = function(m, u, damage, weaponID)":   1, // injection 3 forwarder
	}
	for sub, want := range checks {
		if got := strings.Count(result.Script, sub); got != want {
			t.Errorf("emitted script has %d occurrences of %q, want %d", got, sub, want)
		}
	}
}
