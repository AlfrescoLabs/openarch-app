package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type sample struct {
	Name string `json:"name"`
}

func (s sample) WriteText(w io.Writer) error {
	_, err := fmt.Fprintf(w, "name is %s\n", s.Name)
	return err
}

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"text", "json"} {
		if _, err := ParseFormat(s); err != nil {
			t.Errorf("ParseFormat(%q) error: %v", s, err)
		}
	}
	_, err := ParseFormat("yaml")
	if CodeOf(err) != ExitUsage {
		t.Errorf("ParseFormat(yaml) code = %d, want %d", CodeOf(err), ExitUsage)
	}
}

func TestPrintJSON(t *testing.T) {
	var out bytes.Buffer
	if err := NewPrinter(JSON, &out, io.Discard).Print(sample{Name: "a<b"}); err != nil {
		t.Fatal(err)
	}
	var got sample
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if got.Name != "a<b" || strings.Contains(out.String(), `\u003c`) {
		t.Errorf("unexpected output %q", out.String())
	}
}

func TestPrintText(t *testing.T) {
	var out bytes.Buffer
	if err := NewPrinter(Text, &out, io.Discard).Print(sample{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "name is x\n" {
		t.Errorf("got %q", out.String())
	}
}

func TestErrorJSON(t *testing.T) {
	var out, errw bytes.Buffer
	err := fmt.Errorf("wrapped: %w", NewError(ExitAuth, "login failed", "check the password"))
	NewPrinter(JSON, &out, &errw).Error(err)

	var got struct {
		Error map[string]string `json:"error"`
	}
	if e := json.Unmarshal(out.Bytes(), &got); e != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), e)
	}
	want := map[string]string{"code": "auth", "message": "wrapped: login failed", "hint": "check the password"}
	for k, v := range want {
		if got.Error[k] != v {
			t.Errorf("error.%s = %q, want %q", k, got.Error[k], v)
		}
	}
	if errw.Len() != 0 {
		t.Errorf("stderr should be empty in JSON mode, got %q", errw.String())
	}
}

func TestErrorText(t *testing.T) {
	var out, errw bytes.Buffer
	NewPrinter(Text, &out, &errw).Error(Usage("bad flag", "see help"))
	if out.Len() != 0 {
		t.Errorf("stdout should be empty in text mode, got %q", out.String())
	}
	if errw.String() != "Error: bad flag\nHint: see help\n" {
		t.Errorf("got %q", errw.String())
	}
}

func TestSilentError(t *testing.T) {
	for _, f := range []Format{Text, JSON} {
		var out, errw bytes.Buffer
		err := fmt.Errorf("ctx: %w", Exit(ExitPrerequisite, "checks failed"))
		NewPrinter(f, &out, &errw).Error(err)
		if out.Len() != 0 || errw.Len() != 0 {
			t.Errorf("%s: silent error printed stdout=%q stderr=%q", f, out.String(), errw.String())
		}
		if CodeOf(err) != ExitPrerequisite {
			t.Errorf("%s: code = %d", f, CodeOf(err))
		}
	}
}

func TestCodeOf(t *testing.T) {
	cases := []struct {
		err  error
		want ExitCode
	}{
		{nil, ExitOK},
		{errors.New("plain"), ExitError},
		{NewError(ExitUnhealthy, "x", ""), ExitUnhealthy},
		{fmt.Errorf("ctx: %w", NewError(ExitRemote, "x", "")), ExitRemote},
	}
	for _, c := range cases {
		if got := CodeOf(c.err); got != c.want {
			t.Errorf("CodeOf(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}
