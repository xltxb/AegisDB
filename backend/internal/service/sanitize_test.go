package service

import (
	"strings"
	"testing"
)

// C9: formula injection defusing must look past leading whitespace — spreadsheets
// trim it before parsing, so " =HYPERLINK(...)" is still a live formula.
func TestCsvSanitize_LeadingWhitespace(t *testing.T) {
	cases := map[string]bool{ // input -> should be prefixed with '
		"=HYPERLINK(1)":   true,
		" =HYPERLINK(1)":  true, // leading space then formula
		"\t=cmd":          true, // leading tab then formula
		"  @SUM(A1)":      true,
		"\rvalue":         true, // bare CR lead-in
		"normal":          false,
		"  hello":         false, // whitespace then non-formula
		"":                false,
	}
	for in, wantPrefixed := range cases {
		got := csvSanitize(in)
		gotPrefixed := strings.HasPrefix(got, "'")
		if gotPrefixed != wantPrefixed {
			t.Errorf("csvSanitize(%q) = %q; prefixed=%v want %v", in, got, gotPrefixed, wantPrefixed)
		}
	}
}

// C10: a command/reason embedded in a Lark code fence must not be able to close
// the fence and inject rich text (fake buttons/links). Backticks are neutralized.
func TestLarkSafe_NeutralizesBackticks(t *testing.T) {
	in := "SELECT * FROM `t`; ``` <fake markdown>"
	out := larkSafe(in)
	if strings.Contains(out, "`") {
		t.Errorf("larkSafe(%q) = %q; expected no raw backticks", in, out)
	}
}
