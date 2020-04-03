// Package httpdown provides http.ConnState enabled graceful termination of
// http.Server.
package tcp

import (
	"context"
	"github.com/facebookgo/clock"
	"github.com/facebookgo/stats"
	"net"
	"net/http"
	"reflect"
	"sync"
	"time"
)

const (
	defaultStopTimeout = time.Minute
	defaultKillTimeout = time.Minute
)

type TcpServer struct {
	Server interface{}
}

func (t *TcpServer) ListenAndServe() {
	reflect.ValueOf(t.Server).MethodByName("ListenAndServe").Call([]reflect.Value{})
}
func (t *TcpServer) Shutdown(ctx context.Context) {
	reflect.ValueOf(t.Server).MethodByName("Shutdown").Call([]reflect.Value{
		reflect.ValueOf(ctx),
	})
}
func Gen(server interface{}) *TcpServer {
	return &TcpServer{
		Server: server,
	}
}

type Server interface {
	// Wait waits for the serving loop to finish. This will happen when Stop is
	// called, at which point it returns no error, or if there is an error in the
	// serving loop. You must call Wait after calling Serve or ListenAndServe.
	Wait() error

	// Stop stops the listener. It will block until all connections have been
	// closed.
	Stop() error
}

// server manages the serving process and allows for gracefully stopping it.
type server struct {
	stopTimeout time.Duration
	killTimeout time.Duration
	stats       stats.Client
	clock       clock.Clock

	oldConnState func(net.Conn, http.ConnState)
	server       *http.Server
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
type TCP struct {
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

// Serve provides the low-level API which is useful if you're creating your own
// net.Listener.
//func (t TCP) Serve(s *TcpServer, l net.Listener) Server {
//	stopTimeout := t.StopTimeout
//	if stopTimeout == 0 {
//		stopTimeout = defaultStopTimeout
//	}
//	killTimeout := t.KillTimeout
//	if killTimeout == 0 {
//		killTimeout = defaultKillTimeout
//	}
//	klock := t.Clock
//	if klock == nil {
//		klock = clock.New()
//	}
//
//	ss := &server{
//		stopTimeout:  stopTimeout,
//		killTimeout:  killTimeout,
//		stats:        t.Stats,
//		clock:        klock,
//		oldConnState: s.ConnState,
//		listener:     l,
//		server:       s,
//		serveDone:    make(chan struct{}),
//		serveErr:     make(chan error, 1),
//		new:          make(chan net.Conn),
//		active:       make(chan net.Conn),
//		idle:         make(chan net.Conn),
//		closed:       make(chan net.Conn),
//		stop:         make(chan chan struct{}),
//		kill:         make(chan chan struct{}),
//	}
//	s.ConnState = ss.connState
//	go ss.manage()
//	go ss.serve()
//	return ss
//}