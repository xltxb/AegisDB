package service

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"velagateway/internal/model"
)

// Terminal snippets: short scripts an operator saves and binds to a hotkey.
//
// The one thing to keep straight about this file is what it does NOT do. It does
// not judge SQL, and there is no execute endpoint here. Saving a snippet is
// saving text; running one goes through /risk/check and /terminal/exec exactly as
// typing it does, so the capability matrix, the high-risk dictionary and strict
// mode all see it — against the connection it is aimed at, at the moment it
// fires. A snippet that was harmless on dev when it was written gets judged again
// when the same key is pressed on prod, which is the only order that is safe:
// the hotkey is a typing shortcut, never an authority to run something.

// ErrSnippetLimit is returned when a user is already at MaxSnippets.
var ErrSnippetLimit = fmt.Errorf("snippet limit reached")

// ListSnippets returns the caller's own snippets, hotkey order first.
func (s *Services) ListSnippets(u *model.User) []model.TerminalSnippet {
	if u == nil {
		return []model.TerminalSnippet{}
	}
	ss, _ := s.Repo.ListSnippets(u.ID)
	if ss == nil {
		ss = []model.TerminalSnippet{}
	}
	return ss
}

// SaveSnippet creates (id == 0) or updates one of the caller's own snippets.
//
// An update loads the row and checks the owner before writing. Trusting the id
// alone would let anyone edit anyone's snippet by guessing a number — and since
// the body is what a hotkey submits, that is a way to put SQL under someone
// else's fingers, on instances the author may not even be able to reach.
func (s *Services) SaveSnippet(u *model.User, id int64, name, body string, slot int) (*model.TerminalSnippet, error) {
	if u == nil {
		return nil, ErrForbidden
	}
	name, body, err := validateSnippet(name, body)
	if err != nil {
		return nil, err
	}
	if slot < 0 || slot > model.MaxSnippetSlot {
		return nil, fmt.Errorf("快捷键需在 1-9 之间")
	}

	row := &model.TerminalSnippet{UserID: u.ID, Name: name, Body: body, Slot: slot}
	if id > 0 {
		existing, err := s.Repo.GetSnippet(id)
		if err != nil || existing.UserID != u.ID {
			return nil, ErrForbidden
		}
		row.ID = id
	} else {
		n, err := s.Repo.CountSnippets(u.ID)
		if err != nil {
			return nil, err
		}
		if n >= model.MaxSnippets {
			return nil, ErrSnippetLimit
		}
	}
	if err := s.Repo.SaveSnippet(row); err != nil {
		return nil, err
	}
	// Re-read so the caller gets the stored row: an update writes a subset of the
	// columns, so `row` carries no timestamps and, for a create, the id GORM filled
	// in but not the defaults.
	saved, err := s.Repo.GetSnippet(row.ID)
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// DeleteSnippet removes one of the caller's own snippets.
func (s *Services) DeleteSnippet(u *model.User, id int64) error {
	if u == nil {
		return ErrForbidden
	}
	existing, err := s.Repo.GetSnippet(id)
	if err != nil || existing.UserID != u.ID {
		return ErrForbidden
	}
	return s.Repo.DeleteSnippet(id)
}

// validateSnippet normalises and bounds the two free-text fields.
func validateSnippet(name, body string) (string, string, error) {
	name = strings.TrimSpace(name)
	// Only trailing whitespace goes: leading indentation is part of how a script
	// reads, and the terminal echoes the body as typed.
	body = strings.TrimRight(body, " \t\r\n")
	if name == "" {
		return "", "", fmt.Errorf("名称不能为空")
	}
	if body == "" {
		return "", "", fmt.Errorf("脚本内容不能为空")
	}
	// The name column is VARCHAR(64) — 64 CHARACTERS on MySQL utf8mb4 — so this
	// counts runes. The body column is TEXT, measured in BYTES, so that one counts
	// bytes. Using either rule for both would let input through that the column
	// then truncates: 64 Chinese characters are 192 bytes, and 8,192 Chinese
	// characters are 24,576.
	if utf8.RuneCountInString(name) > model.MaxSnippetName {
		return "", "", fmt.Errorf("名称最长 %d 个字符", model.MaxSnippetName)
	}
	if len(body) > model.MaxSnippetBytes {
		return "", "", fmt.Errorf("脚本内容最大 %d 字节,更大的脚本请用上传方式", model.MaxSnippetBytes)
	}
	return name, body, nil
}
