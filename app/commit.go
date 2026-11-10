package main

import (
	"bytes"
	"fmt"
	"os"
	"time"
)

// now is the clock used for commit timestamps; tests replace it.
var now = time.Now

// signature identifies who authored or committed a commit, and when.
type signature struct {
	Name  string
	Email string
	When  time.Time
}

// String renders the signature the way git stores it:
// "<name> <<email>> <seconds since epoch> <timezone offset>".
func (s signature) String() string {
	return fmt.Sprintf("%s <%s> %d %s", s.Name, s.Email, s.When.Unix(), s.When.Format("-0700"))
}

// envOr returns the value of the environment variable key, or fallback when
// it is unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// defaultSignature builds a signature for the given role ("AUTHOR" or
// "COMMITTER"), honouring git's GIT_<ROLE>_NAME / GIT_<ROLE>_EMAIL
// environment variables.
func defaultSignature(role string) signature {
	return signature{
		Name:  envOr("GIT_"+role+"_NAME", "John Doe"),
		Email: envOr("GIT_"+role+"_EMAIL", "john@example.com"),
		When:  now(),
	}
}

// encodeCommit serializes a commit object's content.
func encodeCommit(tree string, parents []string, author, committer signature, message string) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "tree %s\n", tree)
	for _, p := range parents {
		fmt.Fprintf(&buf, "parent %s\n", p)
	}
	fmt.Fprintf(&buf, "author %s\n", author)
	fmt.Fprintf(&buf, "committer %s\n", committer)
	fmt.Fprintf(&buf, "\n%s\n", message)
	return buf.Bytes()
}

// isHexHash reports whether s is a 40-character lowercase hex object hash.
func isHexHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
