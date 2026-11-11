package cli

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// byteOrderMark is U+FEFF. Windows PowerShell prefixes piped UTF-8 text
// with it, which would otherwise end up inside passwords, URLs, and answers.
const byteOrderMark = string(rune(0xFEFF))

// readLine reads one line from r without its line ending or a leading byte
// order mark. Reaching EOF is not an error; the text read so far is returned.
func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	return strings.TrimPrefix(line, byteOrderMark), nil
}
