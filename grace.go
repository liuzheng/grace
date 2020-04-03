// Package gracehttp provides easy to use graceful restart
// functionality for HTTP server.
package grace

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/liuzheng/grace/pkg/gracenet"
	"github.com/liuzheng/grace/pkg/tcp"
)

var (
	logger     *log.Logger
	didInherit = os.Getenv("LISTEN_FDS") != ""
	ppid       = os.Getppid()
)

type option func(*app)

// An app contains one or more servers and associated configuration.
type app struct {
	servers []interface{}

	//UdpServers  []*net.UDPConn
	TCPServers  []*tcp.TcpServer
	unixServers []*net.UnixListener

	tcp *tcp.TCP
	net *gracenet.Net

	listeners    []net.Listener
	tcpListeners []net.Listener

	TcpDownServer []tcp.Server
	//tcpDownServer   []tcp.Server
	preStartProcess func() error
	preKillProcess  func() error
	postKilledChild func() error
	errors          chan error
}

func newApp(servers []interface{}) *app {
	App := app{
		servers:   servers,
		tcp:       &tcp.TCP{},
		net:       &gracenet.Net{},
		listeners: make([]net.Listener, 0, len(servers)),
		//sds:       make([]httpdown.Server, 0, len(servers)),

		preStartProcess: func() error { return nil },
		preKillProcess:  func() error { return nil },
		postKilledChild: func() error { return nil },
		// 2x num servers for possible Close or Stop errors + 1 for possible
		// StartProcess error.
		errors: make(chan error, 1+(len(servers)*2)),
	}
	for _, server := range servers {
		App.TCPServers = append(App.TCPServers, tcp.Gen(server))
		//switch reflect.TypeOf(v).String() {
		//case "*tcp.TcpServer":
		//	App.TCPServers = append(App.TCPServers, v.(*tcp.TcpServer))
		//case "*http.Server":
		//	App.httpServers = append(App.httpServers, v.(*http.Server))
		//case "*net.UDPConn":
		//	App.udpServers = append(App.udpServers, v.(*net.UDPConn))
		//case "*net.UnixListener":
		//	App.unixServers = append(App.unixServers, v.(*net.UnixListener))
		//default:
		//	fmt.Println("[Error] Unknown Server Type! Abandon!")
		//}
	}
	//App.listeners = make([]net.Listener, 0, len(App.httpServers))
	//App.tcpListeners = make([]net.Listener, 0, len(App.TCPServers))
	//App.httpDownServer = make([]httpdown.Server, 0, len(App.httpServers))
	//App.tcpDownServer = make([]tcp.Server, 0, len(App.TCPServers))
	//app.udpServers=     make([]*net.UDPConn, 0, len_udp),

	return &App
}

//func (a *app) listen() error {
//	for _, s := range a.TCPServers {
//		l, err := a.net.Listen("tcp", s.Addr)
//		if err != nil {
//			return err
//		}
//		if s.TLSConfig != nil {
//			l = tls.NewListener(l, s.TLSConfig)
//		}
//		a.listeners = append(a.listeners, l)
//	}
//	return nil
//}

//func (a *app) serve() {
//	for i, s := range a.TCPServers {
//		a.TcpDownServer = append(a.TcpDownServer, a.tcp.Serve(s, a.listeners[i]))
//	}
//}
func (a *app) listenAndServe() {
	for _, s := range a.TCPServers {
		s.ListenAndServe()
	}
}

func (a *app) wait() {
	var wg sync.WaitGroup
	wg.Add(len(a.TcpDownServer)) // Wait & Stop
	go a.signalHandler(&wg)
	//for _, s := range a.TcpDownServer {
	//	go func(s tcp.Server) {
	//		defer wg.Done()
	//		if err := s.Wait(); err != nil {
	//			a.errors <- err
	//		}
	//	}(s)
	//}
	wg.Wait()
}

func (a *app) shutdown(wg *sync.WaitGroup) {
	for _, s := range a.TCPServers {
		go func(s *tcp.TcpServer) {
			defer wg.Done()
			ctx, _ := context.WithTimeout(context.Background(), 20*time.Second)
			s.Shutdown(ctx)
			//if err := s.Stop(); err != nil {
			//	a.errors <- err
			//}
		}(s)
	}
}

func (a *app) signalHandler(wg *sync.WaitGroup) {
	ch := make(chan os.Signal, 10)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR2)
	for {
		sig := <-ch
		switch sig {
		case syscall.SIGINT, syscall.SIGTERM:
			// this ensures a subsequent INT/TERM will trigger standard go behaviour of
			// terminating.
			signal.Stop(ch)
			a.shutdown(wg)
			return
		case syscall.SIGUSR2:
			err := a.preStartProcess()
			if err != nil {
				a.errors <- err
			}
			// we only return here if there's an error, otherwise the new process
			// will send us a TERM when it's ready to trigger the actual shutdown.
			if _, err := a.net.StartProcess(); err != nil {
				a.errors <- err
			}
		}
	}
}

func (a *app) run() error {
	go a.listenAndServe()

	// Close the parent if we inherited and it wasn't init that started us.
	if didInherit && ppid != 1 {
		if err := syscall.Kill(ppid, syscall.SIGTERM); err != nil {
			return fmt.Errorf("failed to close parent: %s", err)
		}
	}

	waitdone := make(chan struct{})
	go func() {
		defer close(waitdone)
		a.wait()
	}()

	select {
	case err := <-a.errors:
		if err == nil {
			panic("unexpected nil error")
		}
		return err
	case <-waitdone:
		if logger != nil {
			logger.Printf("Exiting pid %d.", os.Getpid())
		}
		return nil
	}
}

// ServeWithOptions does the same as Serve, but takes a set of options to
// configure the app struct.
//func ServeWithOptions(servers []interface{}, options ...option) error {
//	a := newApp(servers)
//	for _, opt := range options {
//		opt(a)
//	}
//	return a.run()
//}

// Serve will serve the given http.Servers and will monitor for signals
// allowing for graceful termination (SIGTERM) or restart (SIGUSR2).
func Serve(servers ...interface{}) {
	a := newApp(servers)
	a.run()
}

// PreStartProcess configures a callback to trigger during graceful restart
// directly before starting the successor process. This allows the current
// process to release holds on resources that the new process will need.
func PreStartProcess(hook func() error) option {
	return func(a *app) {
		a.preStartProcess = hook
	}
}

// Used for pretty printing addresses.
func (a *app) pprintAddr() []byte {
	var out bytes.Buffer
	fmt.Fprint(&out, "[TCP]")
	for i, l := range a.listeners {
		if i != 0 {
			fmt.Fprint(&out, ", ")
		}
		fmt.Fprint(&out, l.Addr())
	}
	//fmt.Fprint(&out, " [UDP]")
	//for i, l := range a.udpServers {
	//	if i != 0 {
	//		fmt.Fprint(&out, ", ")
	//	}
	//	fmt.Fprint(&out, l.LocalAddr())
	//}
	return out.Bytes()
}
