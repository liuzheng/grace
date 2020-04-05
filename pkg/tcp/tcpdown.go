package tcp

import (
  "context"
  "net"
  "reflect"
)

type Tcp interface {
  Serve(l net.Listener) error
  Shutdown(ctx context.Context) error
}

type TcpServer struct {
  Server interface{}
  Addr   string
}

func (t *TcpServer) Serve(l net.Listener) error {
  return t.Server.(Tcp).Serve(l)
  //reflect.ValueOf(t.Server).MethodByName("Serve").Call([]reflect.Value{
  //  reflect.ValueOf(l),
  //})
}
func (t *TcpServer) Shutdown(ctx context.Context) error {
  return t.Server.(Tcp).Shutdown(ctx)
  //reflect.ValueOf(t.Server).MethodByName("Shutdown").Call([]reflect.Value{
  //  reflect.ValueOf(ctx),
  //})
}

func Gen(server interface{}) *TcpServer {
  return &TcpServer{
    Server: server,
    Addr:   reflect.ValueOf(server).Elem().FieldByName("Addr").String(),
  }
}
