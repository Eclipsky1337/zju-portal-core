package atrust

import (
	"context"
	"io"
	"sync"
)

type L3Conn struct {
	l3Tunnel *L3Tunnel
	ctx      context.Context
	sendLock sync.Mutex
	recvLock sync.Mutex
}

func (c *L3Conn) Read(p []byte) (n int, err error) {
	c.recvLock.Lock()
	defer c.recvLock.Unlock()
	select {
	case data := <-c.l3Tunnel.dataChan:
		n = copy(p, data)
		return n, nil
	case <-c.ctx.Done():
		return 0, connectionContextError(c.ctx)
	}
}

func (c *L3Conn) Write(p []byte) (n int, err error) {
	c.sendLock.Lock()
	defer c.sendLock.Unlock()
	if err := connectionContextError(c.ctx); err != nil {
		return 0, err
	}
	if err := c.l3Tunnel.processIPV4(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *L3Conn) Close() error {
	c.l3Tunnel.Close()
	return nil
}

func (t *L3Tunnel) NewL3Conn() (io.ReadWriteCloser, error) {
	conn := &L3Conn{
		l3Tunnel: t,
		ctx:      t.ctx,
	}

	return conn, nil
}

func connectionContextError(ctx context.Context) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return nil
}
