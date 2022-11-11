package grace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/liuzheng/grace/pkg/gracenet"
)

var (
	didInherit = os.Getenv("LISTEN_FDS") != ""
	ppid       = os.Getppid()
)

type GraceServer interface {
	Listen() error
	Serve() error
	Shutdown(ctx context.Context) error
}

// An app contains one or more servers and associated configuration.
type app struct {
	servers []*GraceServer

	//unixServers []*net.UnixListener

	net *gracenet.Net

	//listeners map[string]net.Listener
	// listeners []net.Listener

	preStartProcess func() error
	preKillProcess  func() error
	postKilledChild func() error
	errors          chan error
}

func newApp(servers []*GraceServer) *app {
	return &app{
		servers: servers,
		net:     &gracenet.Net{},

		preStartProcess: func() error { return nil },
		preKillProcess:  func() error { return nil },
		postKilledChild: func() error { return nil },
		// 2x num servers for possible Close or Stop errors + 1 for possible
		// StartProcess error.
		errors: make(chan error, 1+(len(servers)*2)),
	}
}
func (a *app) listen() error {
	for _, s := range a.servers {
		err := s.Listen()
		if err != nil {
			return err
		}
		//if s.TLSConfig != nil {
		//  l = tls.NewListener(l, s.TLSConfig)
		//}
	}
	return nil
}
func (a *app) serve() {
	for _, s := range a.servers {
		go s.Serve()
	}
}

func (a *app) listenAndServe() error {
	// Acquire Listeners
	if err := a.listen(); err != nil {
		return err
	}
	a.serve()

	return nil
}

func (a *app) wait() {
	var wg sync.WaitGroup
	wg.Add(len(a.servers)) // Wait & Stop
	go a.signalHandler(&wg)
	wg.Wait()
}

func (a *app) shutdown(wg *sync.WaitGroup) {
	for _, s := range a.servers {
		go func(s GraceServer) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			s.Shutdown(ctx)
		}(*s)
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

// Serve will serve the given http.Servers and will monitor for signals
// allowing for graceful termination (SIGTERM) or restart (SIGUSR2).
func Serve(servers ...interface{}) error {
	Servers := []*GraceServer{}
	if len(servers) > 0 {
		for _, s := range servers {
			switch x := s.(type) {
			case []interface{}:
				for _, ss := range x {
					Servers = append(Servers, ss.(*GraceServer))
				}
			case interface{}:
				Servers = append(Servers, x.(*GraceServer))
			default:
				return errors.New("unknown type error")
			}
		}
	} else {
		return errors.New("empty servers")
	}
	a := newApp(Servers)

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
		fmt.Printf("Exiting pid %d.\r\n", os.Getpid())
		return nil
	}
}
