package service

import (
	"strings"
	"testing"
)

// C9: formula injection defusing must look past leading whitespace — spreadsheets
// trim it before parsing, so " =HYPERLINK(...)" is still a live formula.
func TestCsvSanitize_LeadingWhitespace(t *testing.T) {
	cases := map[string]bool{ // input -> should be prefixed with '
		"=HYPERLINK(1)":  true,
		" =HYPERLINK(1)": true, // leading space then formula
		"\t=cmd":         true, // leading tab then formula
		"  @SUM(A1)":     true,
		"\rvalue":        true, // bare CR lead-in
		"normal":         false,
		"  hello":        false, // whitespace then non-formula
		"":               false,
	}
	for in, wantPrefixed := range cases {
		got := csvSanitize(in)
		gotPrefixed := strings.HasPrefix(got, "'")
		if gotPrefixed != wantPrefixed {
			t.Errorf("csvSanitize(%q) = %q; prefixed=%v want %v", in, got, gotPrefixed, wantPrefixed)
		}
	}
}
