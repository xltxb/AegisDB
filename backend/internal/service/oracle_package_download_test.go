package service

import "testing"

func TestFormatPck(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"spec+body from ALL_SOURCE gets the final slash",
			"CREATE OR REPLACE PACKAGE P IS\nEND P;\n/\n\nCREATE OR REPLACE PACKAGE BODY P IS\nEND P;",
			"CREATE OR REPLACE PACKAGE P IS\nEND P;\n/\n\nCREATE OR REPLACE PACKAGE BODY P IS\nEND P;\n/\n"},
		{"already terminated is left alone", "CREATE OR REPLACE PACKAGE P IS\nEND P;\n/", "CREATE OR REPLACE PACKAGE P IS\nEND P;\n/\n"},
		{"trailing blank lines are dropped", "END P;\n\n\n  \n", "END P;\n/\n"},
		{"CRLF is normalised", "END P;\r\n/\r\n", "END P;\n/\n"},
		{"a slash inside code is not a terminator", "END P; -- a/b", "END P; -- a/b\n/\n"},
		{"empty stays empty", "  \n", ""},
	}
	for _, c := range cases {
		if got := FormatPck(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestSafeFileStem(t *testing.T) {
	cases := map[string]string{
		"PKG_BILLING":  "PKG_BILLING",
		"../../etc/pw": "_.._etc_pw",
		`a\b:c*d?"e"`:  "a_b_c_d__e_",
		"  ":           "package",
		"..":           "package",
		"包_中文":         "包_中文",
		"x\x00y":       "x_y",
	}
	for in, want := range cases {
		if got := safeFileStem(in); got != want {
			t.Errorf("safeFileStem(%q) = %q, want %q", in, got, want)
		}
	}
}
