// Package testhttp runs httptest servers over net.Pipe when bind is unavailable.
package testhttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
)

type Network struct {
	mu        sync.Mutex
	listeners map[string]*listener
}

func New() *Network { return &Network{listeners: map[string]*listener{}} }
func (n *Network) Unstarted(handler http.Handler) *httptest.Server {
	n.mu.Lock()
	defer n.mu.Unlock()
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000 + len(n.listeners)}
	l := &listener{address: addr, connections: make(chan net.Conn), done: make(chan struct{})}
	n.listeners[addr.String()] = l
	return &httptest.Server{Listener: l, Config: &http.Server{Handler: handler}}
}
func (n *Network) Server(handler http.Handler) *httptest.Server {
	s := n.Unstarted(handler)
	s.Start()
	return s
}
func (n *Network) Transport() *http.Transport {
	return &http.Transport{DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
		n.mu.Lock()
		l := n.listeners[address]
		n.mu.Unlock()
		if l == nil {
			return nil, fmt.Errorf("unknown test origin")
		}
		return l.dial(ctx)
	}}
}

type listener struct {
	address     net.Addr
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func (l *listener) Addr() net.Addr { return l.address }
func (l *listener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.connections:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *listener) dial(ctx context.Context) (net.Conn, error) {
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
