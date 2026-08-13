package bootstrap

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Terminal snippets: saved scripts bound to hotkeys 1-9.
//
// The property worth stating once, because none of the tests below can show it:
// there is no execute route here. A hotkey submits the snippet's text through
// /risk/check and /terminal/exec, so the capability matrix, the high-risk
// dictionary and strict mode judge it against the instance it is aimed at, when
// it fires. What these tests do cover is that the STORE cannot become a way
// around that — one user's snippets are not reachable, readable or writable by
// another, since the body is what lands under someone's fingers on a hotkey.

type snippetRow struct {
	ID     int64  `json:"id"`
	UserID int64  `json:"userId"`
	Name   string `json:"name"`
	Body   string `json:"body"`
	Slot   int    `json:"slot"`
}

func (a *testApp) snippets(token string) []snippetRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/snippets", token, nil)
	eq(a.t, r.Code, 0, "list snippets")
	var rows []snippetRow
	_ = json.Unmarshal(r.Data, &rows)
	return rows
}

func (a *testApp) saveSnippet(token, name, body string, slot int) apiResp {
	a.t.Helper()
	return a.do(http.MethodPost, "/api/v1/snippets", token,
		map[string]any{"name": name, "body": body, "slot": slot})
}

func snippetID(t *testing.T, r apiResp) int64 {
	t.Helper()
	var s snippetRow
	if err := json.Unmarshal(r.Data, &s); err != nil {
		t.Fatalf("decode snippet: %v", err)
	}
	return s.ID
}

func TestSnippets_CreateListAndHotkeyOrder(t *testing.T) {
	app := newTestApp(t)
	tok := app.login("linwei@vela.io", "vela123")

	eq(t, len(app.snippets(tok)), 0, "a new user starts with no snippets")

	eq(t, app.saveSnippet(tok, "未绑定", "SELECT 1;", 0).Code, 0, "save unbound")
	eq(t, app.saveSnippet(tok, "三号", "SELECT 3;", 3).Code, 0, "save slot 3")
	eq(t, app.saveSnippet(tok, "一号", "SELECT 1;", 1).Code, 0, "save slot 1")

	rows := app.snippets(tok)
	eq(t, len(rows), 3, "three snippets")
	// Bound keys first in key order, so the list reads in the order they are
	// pressed rather than the order they were written.
	eq(t, rows[0].Slot, 1, "first row is hotkey 1")
	eq(t, rows[1].Slot, 3, "second row is hotkey 3")
	eq(t, rows[2].Slot, 0, "unbound snippets come last")
	eq2(t, rows[0].Name, "一号", "hotkey 1's name")
}

// Binding a hotkey that is taken releases the previous holder. Two snippets on
// one key would leave the key running whichever row came back first.
func TestSnippets_BindingATakenHotkeyReleasesTheOther(t *testing.T) {
	app := newTestApp(t)
	tok := app.login("linwei@vela.io", "vela123")

	first := snippetID(t, app.saveSnippet(tok, "旧的", "SELECT 'old';", 1))
	eq(t, app.saveSnippet(tok, "新的", "SELECT 'new';", 1).Code, 0, "claim hotkey 1")

	rows := app.snippets(tok)
	eq(t, len(rows), 2, "both snippets still exist — the loser is unbound, not deleted")
	bound := 0
	for _, r := range rows {
		if r.Slot == 1 {
			bound++
			eq2(t, r.Name, "新的", "hotkey 1 belongs to the snippet that claimed it")
		}
		if r.ID == first {
			eq(t, r.Slot, 0, "the previous holder was released")
		}
	}
	eq(t, bound, 1, "exactly one snippet answers to hotkey 1")
}

// Clearing a hotkey has to persist. Slot's zero value is meaningful here, and
// GORM's Updates() skips zero-valued struct fields — so an unbind written that
// way looks like it worked and leaves the key bound.
func TestSnippets_UnbindingAHotkeyPersists(t *testing.T) {
	app := newTestApp(t)
	tok := app.login("linwei@vela.io", "vela123")

	id := snippetID(t, app.saveSnippet(tok, "统计", "SELECT count(*) FROM orders;", 2))
	r := app.do(http.MethodPut, "/api/v1/snippets/"+strconv.FormatInt(id, 10), tok,
		map[string]any{"name": "统计", "body": "SELECT count(*) FROM orders;", "slot": 0})
	eq(t, r.Code, 0, "unbind")

	rows := app.snippets(tok)
	eq(t, len(rows), 1, "still one snippet")
	eq(t, rows[0].Slot, 0, "the hotkey is really gone")
}

// A snippet body is what a hotkey puts under someone's fingers. Reaching another
// user's row would be a way to aim SQL at instances the author cannot even see.
func TestSnippets_AreNotReachableByAnotherUser(t *testing.T) {
	app := newTestApp(t)
	owner := app.login("linwei@vela.io", "vela123")
	other := app.login("zhangwei@vela.io", "vela123")

	id := snippetID(t, app.saveSnippet(owner, "我的", "SELECT 1;", 1))

	eq(t, len(app.snippets(other)), 0, "another user's list does not show it")

	edit := app.do(http.MethodPut, "/api/v1/snippets/"+strconv.FormatInt(id, 10), other,
		map[string]any{"name": "被改了", "body": "DROP TABLE orders;", "slot": 1})
	if edit.Code == 0 {
		t.Error("another user must not be able to rewrite the body of someone's hotkey")
	}
	del := app.do(http.MethodDelete, "/api/v1/snippets/"+strconv.FormatInt(id, 10), other, nil)
	if del.Code == 0 {
		t.Error("another user must not be able to delete someone's snippet")
	}

	rows := app.snippets(owner)
	eq(t, len(rows), 1, "the owner still has it")
	eq2(t, rows[0].Body, "SELECT 1;", "…with the body untouched")

	// The owner can, of course.
	eq(t, app.do(http.MethodDelete, "/api/v1/snippets/"+strconv.FormatInt(id, 10), owner, nil).Code, 0, "owner deletes")
	eq(t, len(app.snippets(owner)), 0, "and it is gone")
}

// The body cap counts BYTES, because the column is measured in bytes. Counting
// characters would accept three times the text MySQL's TEXT column can hold, and
// the excess would be truncated on write — silently, in a script that later runs.
func TestSnippets_BodyCapCountsBytesNotCharacters(t *testing.T) {
	app := newTestApp(t)
	tok := app.login("linwei@vela.io", "vela123")

	// 4,000 Chinese characters: well under 8,192 CHARACTERS, but 12,000 BYTES.
	tooBig := "-- " + strings.Repeat("注", 4000) + "\nSELECT 1;"
	r := app.saveSnippet(tok, "超大", tooBig, 0)
	if r.Code == 0 {
		t.Error("a body over the byte cap must be refused, not stored truncated")
	}
	if !strings.Contains(r.Msg, "8192") {
		t.Errorf("the refusal should say what the limit is, got %q", r.Msg)
	}

	// The same character count, comfortably inside the cap, is fine.
	ok := "-- " + strings.Repeat("注", 1000) + "\nSELECT 1;"
	eq(t, app.saveSnippet(tok, "正常", ok, 0).Code, 0, "a body inside the byte cap is stored")
	eq2(t, app.snippets(tok)[0].Body, ok, "…verbatim")
}

func TestSnippets_RejectEmptyAndOutOfRangeHotkey(t *testing.T) {
	app := newTestApp(t)
	tok := app.login("linwei@vela.io", "vela123")

	if app.saveSnippet(tok, "  ", "SELECT 1;", 0).Code == 0 {
		t.Error("a nameless snippet is unusable in the picker — refuse it")
	}
	if app.saveSnippet(tok, "空的", "   \n  ", 0).Code == 0 {
		t.Error("an empty body would bind a hotkey to nothing")
	}
	if app.saveSnippet(tok, "越界", "SELECT 1;", 10).Code == 0 {
		t.Error("there are nine hotkeys; slot 10 cannot be pressed")
	}
}
