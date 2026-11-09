package output

import "errors"

// ExitCode is the process exit code. The values are part of the public CLI contract.
type ExitCode int

const (
	ExitOK           ExitCode = 0
	ExitError        ExitCode = 1 // generic error
	ExitUsage        ExitCode = 2 // bad usage or input
	ExitPrerequisite ExitCode = 3 // prerequisite missing
	ExitUnhealthy    ExitCode = 4 // service not healthy
	ExitRemote       ExitCode = 5 // remote API error
	ExitAuth         ExitCode = 6 // authentication failed
)

// codeNames are the stable values of the "code" key in JSON errors.
var codeNames = map[ExitCode]string{
	ExitOK:           "ok",
	ExitError:        "error",
	ExitUsage:        "usage",
	ExitPrerequisite: "prerequisite",
	ExitUnhealthy:    "unhealthy",
	ExitRemote:       "remote",
	ExitAuth:         "auth",
}

// ExitCodeInfo describes one exit code.
type ExitCodeInfo struct {
	Code    int    `json:"code"`
	Name    string `json:"name"`
	Meaning string `json:"meaning"`
}

// ExitCodes lists every exit code, in order.
func ExitCodes() []ExitCodeInfo {
	meanings := []string{"OK", "Error", "Bad usage or input", "A prerequisite is missing",
		"A service is not healthy", "Remote API error", "Authentication failed"}
	list := make([]ExitCodeInfo, len(meanings))
	for i, m := range meanings {
		list[i] = ExitCodeInfo{Code: i, Name: ExitCode(i).Name(), Meaning: m}
	}
	return list
}

// Name returns the stable name of the exit code.
func (c ExitCode) Name() string {
	if n, ok := codeNames[c]; ok {
		return n
	}
	return codeNames[ExitError]
}

// Error is an error with an exit code and an optional hint that tells the user how to fix it.
type Error struct {
	Code    ExitCode
	Message string
	Hint    string
	Err     error
	// Silent errors only set the exit code. The command has already printed
	// its result, and printing an error too would break the one-JSON-document rule.
	Silent bool
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Err }

// NewError returns an error with the given exit code.
func NewError(code ExitCode, message, hint string) *Error {
	return &Error{Code: code, Message: message, Hint: hint}
}

// Usage returns an error for bad usage or input.
func Usage(message, hint string) *Error {
	return NewError(ExitUsage, message, hint)
}

// Exit returns a silent error that only sets the exit code.
func Exit(code ExitCode, message string) *Error {
	return &Error{Code: code, Message: message, Silent: true}
}

// CodeOf returns the exit code for err. Errors that are not *Error map to ExitError.
func CodeOf(err error) ExitCode {
	if err == nil {
		return ExitOK
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ExitError
}
