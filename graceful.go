package upgrade

import (
	"net"
	"os"
	"sync"
	"time"
)

//gracefully closing net.Listener
type upListener struct {
	net.Listener
	wg sync.WaitGroup
}

func (l *upListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.(*net.TCPListener).AcceptTCP()
	if err != nil {
		return nil, err
	}
	conn.SetKeepAlive(true)                  // see http.tcpKeepAliveListener
	conn.SetKeepAlivePeriod(3 * time.Minute) // see http.tcpKeepAliveListener
	uconn := upConn{
		Conn: conn,
		wg:   &l.wg,
	}
	l.wg.Add(1)
	return uconn, nil
}

func (l *upListener) File() *os.File {
	// returns a dup(2) - FD_CLOEXEC flag *not* set
	tl := l.Listener.(*net.TCPListener)
	fl, _ := tl.File()
	return fl
}

type upConn struct {
	net.Conn
	wg *sync.WaitGroup
}

func (uconn upConn) Close() error {
	err := uconn.Conn.Close()
	if err == nil {
		uconn.wg.Done()
	}
	return err
}
