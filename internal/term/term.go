// Package term detects terminals.
package term

import (
	"errors"

	"golang.org/x/term"
)

type fder interface {
	Fd() uintptr
}

// IsTerminal reports whether v is a file connected to a terminal.
func IsTerminal(v any) bool {
	f, ok := v.(fder)
	return ok && term.IsTerminal(int(f.Fd()))
}

// ReadPassword reads a line from the terminal in, without echo. It writes
// prompt to out first, and a newline after the answer.
func ReadPassword(in any, out interface{ Write([]byte) (int, error) }, prompt string) (string, error) {
	f, ok := in.(fder)
	if !ok {
		return "", errNotTerminal
	}
	if _, err := out.Write([]byte(prompt)); err != nil {
		return "", err
	}
	b, err := term.ReadPassword(int(f.Fd()))
	_, _ = out.Write([]byte("\n"))
	return string(b), err
}

var errNotTerminal = errors.New("not a terminal")
