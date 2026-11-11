package securepipe

import (
	"regexp"
)

const MaxSecretBytes = 64 * 1024

var validName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func ValidName(name string) bool {
	return validName.MatchString(name)
}
