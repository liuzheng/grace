// Package gracehttp provides easy to use graceful restart
// functionality for HTTP server.
package grace

import (
  "context"
  "fmt"
  "log"
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
  TCPServers []*tcp.TcpServer
  //unixServers []*net.UnixListener

  net *gracenet.Net

  //listeners map[string]net.Listener
  //tcpListeners []net.Listener

  preStartProcess func() error
  preKillProcess  func() error
  postKilledChild func() error
  errors          chan error
}

func newApp(servers []interface{}) *app {
  App := app{
    servers:   servers,
    net:       &gracenet.Net{},
    //listeners: map[string]net.Listener{},
    //sds:       make([]httpdown.Server, 0, len(servers)),

    preStartProcess: func() error { return nil },
    preKillProcess:  func() error { return nil },
    postKilledChild: func() error { return nil },
    // 2x num servers for possible Close or Stop errors + 1 for possible
    // StartProcess error.
    errors: make(chan error, 1+(len(servers)*2)),
  }
  for _, server := range servers {
    t := tcp.Gen(server)
    t.Listener, _ = App.net.Listen("tcp", t.Addr)
    App.TCPServers = append(App.TCPServers, t)
    //App.listeners[t.Addr] = t.Listener
  }
  return &App
}

func (a *app) listenAndServe() {
  //var err error
  for _, s := range a.TCPServers {
    s.ListenAndServe()
  }
}

func (a *app) wait() {
  var wg sync.WaitGroup
  wg.Add(len(a.TCPServers)) // Wait & Stop
  go a.signalHandler(&wg)
  wg.Wait()
}

func (a *app) shutdown(wg *sync.WaitGroup) {
  for _, s := range a.TCPServers {
    go func(s *tcp.TcpServer) {
      defer wg.Done()
      ctx, _ := context.WithTimeout(context.Background(), 20*time.Second)
      s.Shutdown(ctx)
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

// Serve will serve the given http.Servers and will monitor for signals
// allowing for graceful termination (SIGTERM) or restart (SIGUSR2).
func Serve(servers ...interface{}) error {
  a := newApp(servers)
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
