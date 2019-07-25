// Package httpdown provides http.ConnState enabled graceful termination of
// http.Server.
package tcp

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/facebookgo/clock"
	"github.com/facebookgo/stats"
)

const (
	defaultStopTimeout = time.Minute
	defaultKillTimeout = time.Minute
)
type ConnState int
const (
	// StateNew represents a new connection that is expected to
	// send a request immediately. Connections begin at this
	// state and then transition to either StateActive or
	// StateClosed.
	StateNew ConnState = iota

	// StateActive represents a connection that has read 1 or more
	// bytes of a request. The Server.ConnState hook for
	// StateActive fires before the request has entered a handler
	// and doesn't fire again until the request has been
	// handled. After the request is handled, the state
	// transitions to StateClosed, StateHijacked, or StateIdle.
	// For HTTP/2, StateActive fires on the transition from zero
	// to one active request, and only transitions away once all
	// active requests are complete. That means that ConnState
	// cannot be used to do per-request work; ConnState only notes
	// the overall state of the connection.
	StateActive

	// StateIdle represents a connection that has finished
	// handling a request and is in the keep-alive state, waiting
	// for a new request. Connections transition from StateIdle
	// to either StateActive or StateClosed.
	StateIdle

	// StateHijacked represents a hijacked connection.
	// This is a terminal state. It does not transition to StateClosed.
	StateHijacked

	// StateClosed represents a closed connection.
	// This is a terminal state. Hijacked connections do not
	// transition to StateClosed.
	StateClosed
)
type TcpServer struct {
	Addr    string // TCP address to listen on, ":http" if empty
	Serve   func(l net.Listener) error
	Handler func(net.Conn) // handler to invoke, http.DefaultServeMux if nil

	// TLSConfig optionally provides a TLS configuration for use
	// by ServeTLS and ListenAndServeTLS. Note that this value is
	// cloned by ServeTLS and ListenAndServeTLS, so it's not
	// possible to modify the configuration with methods like
	// tls.Config.SetSessionTicketKeys. To use
	// SetSessionTicketKeys, use Server.Serve with a TLS Listener
	// instead.
	TLSConfig *tls.Config

	// ReadTimeout is the maximum duration for reading the entire
	// request, including the body.
	//
	// Because ReadTimeout does not let Handlers make per-request
	// decisions on each request body's acceptable deadline or
	// upload rate, most users will prefer to use
	// ReadHeaderTimeout. It is valid to use them both.
	ReadTimeout time.Duration

	// ReadHeaderTimeout is the amount of time allowed to read
	// request headers. The connection's read deadline is reset
	// after reading the headers and the Handler can decide what
	// is considered too slow for the body.
	ReadHeaderTimeout time.Duration

	// WriteTimeout is the maximum duration before timing out
	// writes of the response. It is reset whenever a new
	// request's header is read. Like ReadTimeout, it does not
	// let Handlers make decisions on a per-request basis.
	WriteTimeout time.Duration

	// IdleTimeout is the maximum amount of time to wait for the
	// next request when keep-alives are enabled. If IdleTimeout
	// is zero, the value of ReadTimeout is used. If both are
	// zero, ReadHeaderTimeout is used.
	IdleTimeout time.Duration

	// MaxHeaderBytes controls the maximum number of bytes the
	// server will read parsing the request header's keys and
	// values, including the request line. It does not limit the
	// size of the request body.
	// If zero, DefaultMaxHeaderBytes is used.
	MaxHeaderBytes int

	// TLSNextProto optionally specifies a function to take over
	// ownership of the provided TLS connection when an NPN/ALPN
	// protocol upgrade has occurred. The map key is the protocol
	// name negotiated. The Handler argument should be used to
	// handle HTTP requests and will initialize the Request's TLS
	// and RemoteAddr if not already set. The connection is
	// automatically closed when the function returns.
	// If TLSNextProto is not nil, HTTP/2 support is not enabled
	// automatically.
	//TLSNextProto map[string]func(*Server, *tls.Conn, Handler)

	// ConnState specifies an optional callback function that is
	// called when a client connection changes state. See the
	// ConnState type and associated constants for details.
	ConnState func(net.Conn, ConnState)

	// ErrorLog specifies an optional logger for errors accepting
	// connections, unexpected behavior from handlers, and
	// underlying FileSystem errors.
	// If nil, logging is done via the log package's standard logger.
	ErrorLog *log.Logger

	disableKeepAlives int32     // accessed atomically.
	inShutdown        int32     // accessed atomically (non-zero means we're in Shutdown)
	nextProtoOnce     sync.Once // guards setupHTTP2_* init
	nextProtoErr      error     // result of http2.ConfigureServer if used

	mu        sync.Mutex
	listeners map[*net.Listener]struct{}
	//activeConn map[*conn]struct{}
	doneChan   chan struct{}
	onShutdown []func()

}

// A Server allows encapsulates the process of accepting new connections and
// serving them, and gracefully shutting down the listener without dropping
// active connections.
type Server interface {
	// Wait waits for the serving loop to finish. This will happen when Stop is
	// called, at which point it returns no error, or if there is an error in the
	// serving loop. You must call Wait after calling Serve or ListenAndServe.
	Wait() error

	// Stop stops the listener. It will block until all connections have been
	// closed.
	Stop() error
}
type Tcp struct {
	// StopTimeout is the duration before we begin force closing connections.
	// Defaults to 1 minute.
	StopTimeout time.Duration

	// KillTimeout is the duration before which we completely give up and abort
	// even though we still have connected clients. This is useful when a large
	// number of client connections exist and closing them can take a long time.
	// Note, this is in addition to the StopTimeout. Defaults to 1 minute.
	KillTimeout time.Duration

	// Stats is optional. If provided, it will be used to record various metrics.
	Stats stats.Client

	// Clock allows for testing timing related functionality. Do not specify this
	// in production code.
	Clock clock.Clock
}

func (t Tcp) Serve(ts *TcpServer, l net.Listener) Server {
	stopTimeout := t.StopTimeout
	if stopTimeout == 0 {
		stopTimeout = defaultStopTimeout
	}
	killTimeout := t.KillTimeout
	if killTimeout == 0 {
		killTimeout = defaultKillTimeout
	}
	klock := t.Clock
	if klock == nil {
		klock = clock.New()
	}
	ss := &server{
		stopTimeout:  stopTimeout,
		killTimeout:  killTimeout,
		stats:        t.Stats,
		clock:        klock,
		oldConnState: ts.ConnState,
		listener:     l,
		server:       ts,
		serveDone:    make(chan struct{}),
		serveErr:     make(chan error, 1),
		new:          make(chan net.Conn),
		active:       make(chan net.Conn),
		idle:         make(chan net.Conn),
		closed:       make(chan net.Conn),
		stop:         make(chan chan struct{}),
		kill:         make(chan chan struct{}),
	}
	ts.ConnState = ss.connState
	go ss.manage()
	go ss.serve()
	return ss
}

//// HTTP defines the configuration for serving a http.Server. Multiple calls to
//// Serve or ListenAndServe can be made on the same HTTP instance. The default
//// timeouts of 1 minute each result in a maximum of 2 minutes before a Stop()
//// returns.
//type HTTP struct {
//	// StopTimeout is the duration before we begin force closing connections.
//	// Defaults to 1 minute.
//	StopTimeout time.Duration
//
//	// KillTimeout is the duration before which we completely give up and abort
//	// even though we still have connected clients. This is useful when a large
//	// number of client connections exist and closing them can take a long time.
//	// Note, this is in addition to the StopTimeout. Defaults to 1 minute.
//	KillTimeout time.Duration
//
//	// Stats is optional. If provided, it will be used to record various metrics.
//	Stats stats.Client
//
//	// Clock allows for testing timing related functionality. Do not specify this
//	// in production code.
//	Clock clock.Clock
//}
//
//// Serve provides the low-level API which is useful if you're creating your own
//// net.Listener.
//func (h HTTP) Serve(t *TcpServer, l net.Listener) Server {
//	stopTimeout := h.StopTimeout
//	if stopTimeout == 0 {
//		stopTimeout = defaultStopTimeout
//	}
//	killTimeout := h.KillTimeout
//	if killTimeout == 0 {
//		killTimeout = defaultKillTimeout
//	}
//	klock := h.Clock
//	if klock == nil {
//		klock = clock.New()
//	}
//
//	ss := &server{
//		stopTimeout:  stopTimeout,
//		killTimeout:  killTimeout,
//		stats:        h.Stats,
//		clock:        klock,
//		oldConnState: t.ConnState,
//		listener:     l,
//		server:       t,
//		serveDone:    make(chan struct{}),
//		serveErr:     make(chan error, 1),
//		new:          make(chan net.Conn),
//		active:       make(chan net.Conn),
//		idle:         make(chan net.Conn),
//		closed:       make(chan net.Conn),
//		stop:         make(chan chan struct{}),
//		kill:         make(chan chan struct{}),
//	}
//	t.ConnState = ss.connState
//	go ss.manage()
//	go ss.serve()
//	return ss
//}
//
//// ListenAndServe returns a Server for the given http.Server. It is equivalent
//// to ListenAndServe from the standard library, but returns immediately.
//// Requests will be accepted in a background goroutine. If the http.Server has
//// a non-nil TLSConfig, a TLS enabled listener will be setup.
//func (h HTTP) ListenAndServe(t *TcpServer) (Server, error) {
//	addr := t.Addr
//	if addr == "" {
//		//if s.TLSConfig == nil {
//		//	addr = ":http"
//		//} else {
//		//	addr = ":https"
//		//}
//		addr = "127.0.0.1:2222"
//	}
//	l, err := net.Listen("tcp", addr)
//	if err != nil {
//		stats.BumpSum(h.Stats, "listen.error", 1)
//		return nil, err
//	}
//	if t.TLSConfig != nil {
//		l = tls.NewListener(l, t.TLSConfig)
//	}
//	t.Serve(l)
//	return h.Serve(s, l), nil
//}

// server manages the serving process and allows for gracefully stopping it.
type server struct {
	stopTimeout time.Duration
	killTimeout time.Duration
	stats       stats.Client
	clock       clock.Clock

	oldConnState func(net.Conn, ConnState)
	server       *TcpServer
	serveDone    chan struct{}
	serveErr     chan error
	listener     net.Listener

	new    chan net.Conn
	active chan net.Conn
	idle   chan net.Conn
	closed chan net.Conn
	stop   chan chan struct{}
	kill   chan chan struct{}

	stopOnce sync.Once
	stopErr  error
}

func (s *server) connState(c net.Conn, cs ConnState) {
	if s.oldConnState != nil {
		s.oldConnState(c, cs)
	}

	switch cs {
	case StateNew:
		s.new <- c
	case StateActive:
		s.active <- c
	case StateIdle:
		s.idle <- c
	case StateHijacked, StateClosed:
		s.closed <- c
	}
}

func (s *server) manage() {
	defer func() {
		close(s.new)
		close(s.active)
		close(s.idle)
		close(s.closed)
		close(s.stop)
		close(s.kill)
	}()

	var stopDone chan struct{}

	conns := map[net.Conn]http.ConnState{}
	var countNew, countActive, countIdle float64

	// decConn decrements the count associated with the current state of the
	// given connection.
	decConn := func(c net.Conn) {
		switch conns[c] {
		default:
			panic(fmt.Errorf("unknown existing connection: %s", c))
		case http.StateNew:
			countNew--
		case http.StateActive:
			countActive--
		case http.StateIdle:
			countIdle--
		}
	}

	// setup a ticker to report various values every minute. if we don't have a
	// Stats implementation provided, we Stop it so it never ticks.
	statsTicker := s.clock.Ticker(time.Minute)
	if s.stats == nil {
		statsTicker.Stop()
	}

	for {
		select {
		case <-statsTicker.C:
			// we'll only get here when s.stats is not nil
			s.stats.BumpAvg("http-state.new", countNew)
			s.stats.BumpAvg("http-state.active", countActive)
			s.stats.BumpAvg("http-state.idle", countIdle)
			s.stats.BumpAvg("http-state.total", countNew+countActive+countIdle)
		case c := <-s.new:
			conns[c] = http.StateNew
			countNew++
		case c := <-s.active:
			decConn(c)
			countActive++

			conns[c] = http.StateActive
		case c := <-s.idle:
			decConn(c)
			countIdle++

			conns[c] = http.StateIdle

			// if we're already stopping, close it
			if stopDone != nil {
				c.Close()
			}
		case c := <-s.closed:
			stats.BumpSum(s.stats, "conn.closed", 1)
			decConn(c)
			delete(conns, c)

			// if we're waiting to stop and are all empty, we just closed the last
			// connection and we're done.
			if stopDone != nil && len(conns) == 0 {
				close(stopDone)
				return
			}
		case stopDone = <-s.stop:
			// if we're already all empty, we're already done
			if len(conns) == 0 {
				close(stopDone)
				return
			}

			// close current idle connections right away
			for c, cs := range conns {
				if cs == http.StateIdle {
					c.Close()
				}
			}

			// continue the loop and wait for all the ConnState updates which will
			// eventually close(stopDone) and return from this goroutine.

		case killDone := <-s.kill:
			// force close all connections
			stats.BumpSum(s.stats, "kill.conn.count", float64(len(conns)))
			for c := range conns {
				c.Close()
			}

			// don't block the kill.
			close(killDone)

			// continue the loop and we wait for all the ConnState updates and will
			// return from this goroutine when we're all done. otherwise we'll try to
			// send those ConnState updates on closed channels.

		}
	}
}

func (s *server) serve() {
	stats.BumpSum(s.stats, "serve", 1)
	s.serveErr <- s.server.Serve(s.listener)
	close(s.serveDone)
	close(s.serveErr)
}

func (s *server) Wait() error {
	if err := <-s.serveErr; !isUseOfClosedError(err) {
		return err
	}
	return nil
}

func (s *server) Stop() error {
	s.stopOnce.Do(func() {
		defer stats.BumpTime(s.stats, "stop.time").End()
		stats.BumpSum(s.stats, "stop", 1)

		// first disable keep-alive for new connections
		// ToDo:
		//s.server.SetKeepAlivesEnabled(false)

		// then close the listener so new connections can't connect come thru
		closeErr := s.listener.Close()
		<-s.serveDone

		// then trigger the background goroutine to stop and wait for it
		stopDone := make(chan struct{})
		s.stop <- stopDone

		// wait for stop
		select {
		case <-stopDone:
		case <-s.clock.After(s.stopTimeout):
			defer stats.BumpTime(s.stats, "kill.time").End()
			stats.BumpSum(s.stats, "kill", 1)

			// stop timed out, wait for kill
			killDone := make(chan struct{})
			s.kill <- killDone
			select {
			case <-killDone:
			case <-s.clock.After(s.killTimeout):
				// kill timed out, give up
				stats.BumpSum(s.stats, "kill.timeout", 1)
			}
		}

		if closeErr != nil && !isUseOfClosedError(closeErr) {
			stats.BumpSum(s.stats, "listener.close.error", 1)
			s.stopErr = closeErr
		}
	})
	return s.stopErr
}

func isUseOfClosedError(err error) bool {
	if err == nil {
		return false
	}
	if opErr, ok := err.(*net.OpError); ok {
		err = opErr.Err
	}
	return err.Error() == "use of closed network connection"
}

// ListenAndServe is a convenience function to serve and wait for a SIGTERM
// or SIGINT before shutting down.
//func ListenAndServe(s *http.Server, hd *HTTP) error {
//	if hd == nil {
//		hd = &HTTP{}
//	}
//	hs, err := hd.ListenAndServe(s)
//	if err != nil {
//		return err
//	}
//
//	waiterr := make(chan error, 1)
//	go func() {
//		defer close(waiterr)
//		waiterr <- hs.Wait()
//	}()
//
//	signals := make(chan os.Signal, 10)
//	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
//
//	select {
//	case err := <-waiterr:
//		if err != nil {
//			return err
//		}
//	case <-signals:
//		signal.Stop(signals)
//		if err := hs.Stop(); err != nil {
//			return err
//		}
//		if err := <-waiterr; err != nil {
//			return err
//		}
//	}
//	return nil
//}
