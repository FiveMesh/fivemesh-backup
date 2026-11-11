package cli

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadLineStripsByteOrderMarkAndLineEnding(t *testing.T) {
	cases := map[string]string{
		byteOrderMark + "secret\r\n": "secret",
		"secret\n":                   "secret",
		"no-newline":                 "no-newline",
		"":                           "",
	}
	for input, want := range cases {
		got, err := readLine(bufio.NewReader(strings.NewReader(input)))
		if err != nil || got != want {
			t.Errorf("readLine(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestInitPasswordFromPowerShellPipeHasNoByteOrderMark(t *testing.T) {
	password, err := readInitialPassword(strings.NewReader(byteOrderMark+"db-password\r\n"), nil, true)
	if err != nil || password != "db-password" {
		t.Fatalf("password = %q, %v", password, err)
	}
}
