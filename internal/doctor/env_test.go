package doctor

import (
	"context"
	"net"
	"testing"
)

func TestPortFree(t *testing.T) {
	// A listener on 127.0.0.1 only: on macOS a wildcard bind on the same port
	// can still succeed, so this case needs the connect test.
	ctx := context.Background()
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if portFree(ctx, port) {
		t.Errorf("port %d reported free while a listener is open", port)
	}
	_ = l.Close()
	if !portFree(ctx, port) {
		t.Errorf("port %d reported in use after the listener closed", port)
	}
}
