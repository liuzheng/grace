// Package gracehttp provides easy to use graceful restart
// functionality for HTTP server.
package grace

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"github.com/liuzheng/grace/gracenet"
	"github.com/liuzheng/grace/httpdown"
	"github.com/liuzheng/grace/tcp"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"strconv"
	"sync"
	"syscall"
	"time"
)

var (
	logger     *log.Logger
	didInherit = os.Getenv("LISTEN_FDS") != ""
	ppid       = os.Getppid()
)

type option func(*app)

// An app contains one or more servers and associated configuration.
type app struct {
	httpServers []*http.Server
	udpServers  []*net.UDPConn
	TCPServers  []*tcp.TcpServer
	unixServers []*net.UnixListener

	http *httpdown.HTTP
	net  *gracenet.Net
	tcp  *tcp.Tcp

	listeners    []net.Listener
	tcpListeners []net.Listener

	httpDownServer  []httpdown.Server
	tcpDownServer   []tcp.Server
	preStartProcess func() error
	//preKillProcess  func() error
	postKilledChild func() error
	errors          chan error
}

func newApp(servers []interface{}) *app {
	App := app{
		http: &httpdown.HTTP{},
		net:  &gracenet.Net{},
		tcp:  &tcp.Tcp{},

		preStartProcess: func() error { return nil },
		//preKillProcess:  func() error { return nil },
		postKilledChild: func() error { return nil },
		// 2x num servers for possible Close or Stop errors + 1 for possible
		// StartProcess error.
		errors: make(chan error, 1+(len(servers)*2)),
	}
	for _, v := range servers {
		switch reflect.TypeOf(v).String() {
		case "*tcp.TcpServer":
			App.TCPServers = append(App.TCPServers, v.(*tcp.TcpServer))
		case "*http.Server":
			App.httpServers = append(App.httpServers, v.(*http.Server))
		case "*net.UDPConn":
			App.udpServers = append(App.udpServers, v.(*net.UDPConn))
		case "*net.UnixListener":
			App.unixServers = append(App.unixServers, v.(*net.UnixListener))
		default:
			fmt.Println("[Error] Unknown Server Type! Abandon!")
		}
	}
	App.listeners = make([]net.Listener, 0, len(App.httpServers))
	App.tcpListeners = make([]net.Listener, 0, len(App.TCPServers))
	App.httpDownServer = make([]httpdown.Server, 0, len(App.httpServers))
	App.tcpDownServer = make([]tcp.Server, 0, len(App.TCPServers))
	//app.udpServers=     make([]*net.UDPConn, 0, len_udp),

	return &App
}

func (a *app) listen() error {
	for _, s := range a.httpServers {
		l, err := a.net.Listen("tcp", s.Addr)
		if err != nil {
			return err
		}
		if s.TLSConfig != nil {
			l = tls.NewListener(l, s.TLSConfig)
		}
		a.listeners = append(a.listeners, l)
	}
	for _, s := range a.TCPServers {
		l, err := a.net.Listen("tcp", s.Addr)
		if err != nil {
			return err
		}
		if s.TLSConfig != nil {
			l = tls.NewListener(l, s.TLSConfig)
		}
		a.tcpListeners = append(a.tcpListeners, l)
	}
	return nil
}

func (a *app) serve() {
	for i, s := range a.httpServers {
		a.httpDownServer = append(a.httpDownServer, a.http.Serve(s, a.listeners[i]))
	}
	for i, s := range a.TCPServers {
		// ToDo:
		a.tcpDownServer = append(a.tcpDownServer, a.tcp.Serve(s, a.tcpListeners[i]))
	}
}

func (a *app) wait() {
	var wg sync.WaitGroup
	wg.Add((len(a.httpDownServer) + len(a.tcpDownServer)) * 2) // Wait & Stop
	go a.signalHandler(&wg)
	for _, s := range a.httpDownServer {
		go func(s httpdown.Server) {
			defer wg.Done()
			if err := s.Wait(); err != nil {
				a.errors <- err
			}
		}(s)
	}
	for _, s := range a.tcpDownServer {
		go func(s tcp.Server) {
			defer wg.Done()
			if err := s.Wait(); err != nil {
				a.errors <- err
			}
		}(s)
	}
	wg.Wait()
}

func (a *app) term(wg *sync.WaitGroup) {
	for _, s := range a.httpDownServer {
		go func(s httpdown.Server) {
			defer wg.Done()
			if err := s.Stop(); err != nil {
				a.errors <- err
			}
		}(s)
	}
	for _, s := range a.tcpDownServer {
		go func(s tcp.Server) {
			defer wg.Done()
			if err := s.Stop(); err != nil {
				a.errors <- err
			}
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
			a.term(wg)
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
			err = a.preKillProcess()
			if err != nil {
				a.errors <- err
			}
		}
	}
}

func (a *app) run() error {
	// Acquire Listeners
	if err := a.listen(); err != nil {
		fmt.Println(err)
		return err
	}

	// Some useful logging.
	if logger != nil {
		if didInherit {
			if ppid == 1 {
				logger.Printf("Listening on init activated %s", a.pprintAddr())
			} else {
				logger.Printf("Graceful handoff of %s with new pid %d and old pid %d", a.pprintAddr(), os.Getpid(), ppid)
			}
		} else {
			logger.Printf("Serving %s with pid %d", a.pprintAddr(), os.Getpid())
		}
	}

	// Start serving.
	a.serve()

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

func (a *app) preKillProcess() (err error) {
	for _, l := range a.tcpListeners {
		l.Close()
	}
	ticker := time.NewTicker(time.Second * 5)
	for range ticker.C {
		lsof := exec.Command("lsof", "-p", strconv.Itoa(os.Getpid()), "-a", "-i", "tcp")
		out, _ := lsof.Output()
		lsofout := string(out)
		if lsofout == "" {
			os.Exit(0)
		}
	}
	return
}

// ServeWithOptions does the same as Serve, but takes a set of options to
// configure the app struct.
func ServeWithOptions(servers []interface{}, options ...option) error {
	a := newApp(servers)
	for _, opt := range options {
		opt(a)
	}
	return a.run()
}

// Serve will serve the given http.Servers and will monitor for signals
// allowing for graceful termination (SIGTERM) or restart (SIGUSR2).
func Serve(servers ...interface{}) error {
	a := newApp(servers)
	return a.run()
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
	fmt.Fprint(&out, "[HTTP]")
	for i, l := range a.listeners {
		if i != 0 {
			fmt.Fprint(&out, ", ")
		}
		fmt.Fprint(&out, l.Addr())
	}
	fmt.Fprint(&out, " [UDP]")
	for i, l := range a.udpServers {
		if i != 0 {
			fmt.Fprint(&out, ", ")
		}
		fmt.Fprint(&out, l.LocalAddr())
	}
	return out.Bytes()
}
