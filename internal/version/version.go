// Package version holds build information. Release builds set the variables with -ldflags -X.
package version

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
)

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Info describes the running binary.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// Get returns the build information. When ldflags are not set (for example with
// `go install`), it falls back to the module and VCS data embedded by the Go toolchain.
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if Version != "dev" {
		return info
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		info.Version = v
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Commit = s.Value
		case "vcs.time":
			info.Date = s.Value
		}
	}
	return info
}

// WriteText writes the human-readable form.
func (i Info) WriteText(w io.Writer) error {
	_, err := fmt.Fprintf(w, "openarch-app %s\n  commit:   %s\n  built:    %s\n  go:       %s\n  platform: %s\n",
		i.Version, i.Commit, i.Date, i.GoVersion, i.Platform)
	return err
}
