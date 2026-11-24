// Package shell quotes words for sh.
package shell

import (
	"regexp"
	"strings"
)

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./~<>-]+$`)

// Quote quotes s for sh, leaving plain words as they are.
func Quote(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func Join(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = Quote(w)
	}
	return strings.Join(quoted, " ")
}
