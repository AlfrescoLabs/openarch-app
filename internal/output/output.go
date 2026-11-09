// Package output prints command results and errors as text or JSON.
//
// Results go to stdout. In JSON mode errors also go to stdout, so a caller
// always reads exactly one JSON document there. In text mode errors go to stderr.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Format is an output format.
type Format string

const (
	Text Format = "text"
	JSON Format = "json"
)

// ParseFormat validates s as an output format.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case Text, JSON:
		return Format(s), nil
	}
	return "", Usage(fmt.Sprintf("invalid output format %q", s), "Use --output text or --output json.")
}

// Texter is implemented by results that have a human-readable form.
type Texter interface {
	WriteText(w io.Writer) error
}

// Printer writes results and errors in one format.
type Printer struct {
	format Format
	out    io.Writer
	errw   io.Writer
}

// NewPrinter returns a printer that writes results to out and text errors to errw.
func NewPrinter(format Format, out, errw io.Writer) *Printer {
	return &Printer{format: format, out: out, errw: errw}
}

// Format returns the printer format.
func (p *Printer) Format() Format { return p.format }

// Print writes a command result.
func (p *Printer) Print(v any) error {
	if p.format == JSON {
		return writeJSON(p.out, v)
	}
	if t, ok := v.(Texter); ok {
		return t.WriteText(p.out)
	}
	_, err := fmt.Fprintln(p.out, v)
	return err
}

// Warn writes a warning to stderr in every format, so JSON stdout is not affected.
func (p *Printer) Warn(msg string) {
	_, _ = fmt.Fprintln(p.errw, "Warning:", msg)
}

type jsonError struct {
	Error jsonErrorBody `json:"error"`
}

type jsonErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// Error writes err. Write failures are ignored because there is nowhere left to report them.
func (p *Printer) Error(err error) {
	body := jsonErrorBody{Code: CodeOf(err).Name(), Message: err.Error()}
	var e *Error
	if errors.As(err, &e) {
		if e.Silent {
			return
		}
		body.Hint = e.Hint
	}
	if p.format == JSON {
		_ = writeJSON(p.out, jsonError{Error: body})
		return
	}
	_, _ = fmt.Fprintln(p.errw, "Error:", body.Message)
	if body.Hint != "" {
		_, _ = fmt.Fprintln(p.errw, "Hint:", body.Hint)
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
