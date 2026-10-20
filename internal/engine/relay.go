package engine

import (
	"errors"
	"io"
	"net"
	"strconv"
	"time"
)

// relayIPv6 listens on [::1]:port and passes each connection to the loopback
// forward on 127.0.0.1:port, so the forward answers on both loopback
// addresses. A browser may ask for either: a dev server on the IPv6 loopback
// names [::1] in the URLs it hands out, as Vite does under Node 17 and
// later. The forward itself stays on 127.0.0.1, where ssh reports a port it
// cannot bind; asked for both, it would leave one out without saying so.
// Closing the listener stops new connections; open ones run to their end.
func relayIPv6(port int) (net.Listener, error) {
	l, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	go func() {
		// An error other than the close, such as running out of file
		// descriptors, passes; the loop waits a little longer each time.
		wait := 5 * time.Millisecond
		for {
			c, err := l.Accept()
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				time.Sleep(wait)
				wait = min(2*wait, time.Second)
				continue
			}
			wait = 5 * time.Millisecond
			go relayConn(c, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		}
	}()
	return l, nil
}

// relayConn copies c to and from a new connection to addr until both sides
// are done, passing on a half close either way.
func relayConn(c net.Conn, addr string) {
	defer c.Close()
	up, err := net.Dial("tcp", addr)
	if err != nil {
		return
	}
	defer up.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(up, c)
		_ = up.(*net.TCPConn).CloseWrite()
	}()
	_, _ = io.Copy(c, up)
	_ = c.(*net.TCPConn).CloseWrite()
	<-done
}
