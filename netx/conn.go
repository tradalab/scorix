package netx

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

type tunnelConn struct {
	net.Conn
	remote net.Addr

	chunks  chan []byte
	readErr error
	pending []byte

	mu         sync.Mutex
	readDL     chan struct{}
	readTimer  *time.Timer
	readNote   chan struct{}
	writeDL    chan struct{}
	writeTimer *time.Timer
	writeNote  chan struct{}
	closed     chan struct{}
	closeOnce  sync.Once
}

type namedAddr struct{ network, addr string }

func (a namedAddr) Network() string { return a.network }
func (a namedAddr) String() string  { return a.addr }

func newTunnelConn(c net.Conn, network, addr string) *tunnelConn {
	tc := &tunnelConn{
		Conn:      c,
		remote:    namedAddr{network, addr},
		chunks:    make(chan []byte),
		readNote:  make(chan struct{}),
		writeNote: make(chan struct{}),
		closed:    make(chan struct{}),
	}
	go tc.pump()
	return tc
}

func (c *tunnelConn) pump() {
	for {
		buf := make([]byte, 32*1024)
		n, err := c.Conn.Read(buf)
		if n > 0 {
			select {
			case c.chunks <- buf[:n]:
			case <-c.closed:
				return
			}
		}
		if err != nil {
			c.readErr = err
			close(c.chunks)
			return
		}
	}
}

func (c *tunnelConn) RemoteAddr() net.Addr { return c.remote }

func (c *tunnelConn) Read(b []byte) (int, error) {
	if len(c.pending) > 0 {
		n := copy(b, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	for {
		c.mu.Lock()
		dl, note := c.readDL, c.readNote
		c.mu.Unlock()
		if isDone(dl) && dl != nil {
			return 0, os.ErrDeadlineExceeded
		}
		select {
		case chunk, ok := <-c.chunks:
			if !ok {
				if c.readErr == nil {
					return 0, io.EOF
				}
				return 0, c.readErr
			}
			n := copy(b, chunk)
			c.pending = chunk[n:]
			return n, nil
		case <-dl:
			return 0, os.ErrDeadlineExceeded
		case <-note:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
}

func (c *tunnelConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	dl, note := c.writeDL, c.writeNote
	c.mu.Unlock()
	if dl == nil {
		return c.Conn.Write(b)
	}
	if isDone(dl) {
		return 0, os.ErrDeadlineExceeded
	}
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := c.Conn.Write(b)
		done <- result{n, err}
	}()
	for {
		select {
		case r := <-done:
			return r.n, r.err
		case <-dl:
			_ = c.Close()
			r := <-done
			return r.n, os.ErrDeadlineExceeded
		case <-note:
			c.mu.Lock()
			dl, note = c.writeDL, c.writeNote
			c.mu.Unlock()
		}
	}
}

func deadlineChan(t time.Time) (chan struct{}, *time.Timer) {
	if t.IsZero() {
		return nil, nil
	}
	ch := make(chan struct{})
	d := time.Until(t)
	if d <= 0 {
		close(ch)
		return ch, nil
	}
	return ch, time.AfterFunc(d, func() { close(ch) })
}

func (c *tunnelConn) SetReadDeadline(t time.Time) error {
	ch, timer := deadlineChan(t)
	c.mu.Lock()
	if c.readTimer != nil {
		c.readTimer.Stop()
	}
	c.readDL, c.readTimer = ch, timer
	close(c.readNote)
	c.readNote = make(chan struct{})
	c.mu.Unlock()
	return nil
}

func (c *tunnelConn) SetWriteDeadline(t time.Time) error {
	ch, timer := deadlineChan(t)
	c.mu.Lock()
	if c.writeTimer != nil {
		c.writeTimer.Stop()
	}
	c.writeDL, c.writeTimer = ch, timer
	close(c.writeNote)
	c.writeNote = make(chan struct{})
	c.mu.Unlock()
	return nil
}

func (c *tunnelConn) SetDeadline(t time.Time) error {
	return errors.Join(c.SetReadDeadline(t), c.SetWriteDeadline(t))
}

func (c *tunnelConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.Conn.Close()
	})
	return err
}
