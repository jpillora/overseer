package overseer

import (
	"net"
	"os"
	"sync"
	"time"
)

func newUpListener(l net.Listener) *upListener {
	return &upListener{
		Listener:     l,
		closeByForce: make(chan bool),
	}
}

//gracefully closing net.Listener
type upListener struct {
	net.Listener
	closeError   error
	closeByForce chan bool
	wg           sync.WaitGroup
}

func (l *upListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.(*net.TCPListener).AcceptTCP()
	if err != nil {
		return nil, err
	}
	conn.SetKeepAlive(true)                  // see http.tcpKeepAliveListener
	conn.SetKeepAlivePeriod(3 * time.Minute) // see http.tcpKeepAliveListener
	uconn := upConn{
		Conn:   conn,
		wg:     &l.wg,
		closed: make(chan bool),
	}
	go func() {
		//connection watcher
		select {
		case <-l.closeByForce:
			uconn.Close()
		case <-uconn.closed:
			//closed manually
		}
	}()
	l.wg.Add(1)
	return uconn, nil
}

//non-blocking trigger close
func (l *upListener) release(timeout time.Duration) {
	//stop accepting connections - release fd
	l.closeError = l.Listener.Close()
	//start timer, close by force if deadline not met
	waited := make(chan bool)
	go func() {
		l.wg.Wait()
		waited <- true
	}()
	go func() {
		select {
		case <-time.After(timeout):
			close(l.closeByForce)
		case <-waited:
			//no need to force close
		}
	}()
}

//blocking wait for close
func (l *upListener) Close() error {
	l.wg.Wait()
	return l.closeError
}

func (l *upListener) File() *os.File {
	// returns a dup(2) - FD_CLOEXEC flag *not* set
	tl := l.Listener.(*net.TCPListener)
	fl, _ := tl.File()
	return fl
}

//notifying on close net.Conn
type upConn struct {
	net.Conn
	wg     *sync.WaitGroup
	closed chan bool
}

func (uconn upConn) Close() error {
	err := uconn.Conn.Close()
	if err == nil {
		uconn.wg.Done()
		uconn.closed <- true
	}
	return err
}
