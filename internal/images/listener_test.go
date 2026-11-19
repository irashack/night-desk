package images

import (
	"context"
	"net"
	"sync"
)

// net.Pipe keeps the HTTP/TLS integration tests runnable in sandboxes that
// prohibit bind(2), without skipping the transport or using external services.
type memoryListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func newMemoryListener() *memoryListener {
	return &memoryListener{connections: make(chan net.Conn), done: make(chan struct{})}
}
func (l *memoryListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443} }
func (l *memoryListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *memoryListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.connections:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *memoryListener) dial(ctx context.Context) (net.Conn, error) {
	a, b := net.Pipe()
	select {
	case l.connections <- b:
		return a, nil
	case <-ctx.Done():
		a.Close()
		b.Close()
		return nil, ctx.Err()
	case <-l.done:
		a.Close()
		b.Close()
		return nil, net.ErrClosed
	}
}
