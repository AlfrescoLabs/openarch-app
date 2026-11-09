package doctor

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// Env is the access to the system used by the checks. Tests replace it with fakes.
type Env struct {
	GOOS     string
	LookPath func(file string) (string, error)
	Run      func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
	PortFree func(ctx context.Context, port int) bool
	// HTTPStatus returns the status code of a GET request, or an error if no response arrived.
	HTTPStatus func(ctx context.Context, url string) (int, error)
	Stat       func(name string) (os.FileInfo, error)
}

// SystemEnv returns an Env backed by the real system.
func SystemEnv() Env {
	return Env{
		GOOS:       runtime.GOOS,
		LookPath:   exec.LookPath,
		Run:        runCommand,
		PortFree:   portFree,
		HTTPStatus: httpStatus,
		Stat:       os.Stat,
	}
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// portFree reports whether nothing listens on the TCP port. Binding alone is not
// enough: on macOS a wildcard bind can succeed while another process holds
// 127.0.0.1 on the same port, so the check also tries to connect.
func portFree(ctx context.Context, port int) bool {
	d := net.Dialer{Timeout: 500 * time.Millisecond}
	if conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
		_ = conn.Close()
		return false
	}
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func httpStatus(ctx context.Context, url string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
