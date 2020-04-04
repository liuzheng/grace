// Package httpdown provides http.ConnState enabled graceful termination of
// http.Server.
package tcp

import (
  "context"
  "net"
  "reflect"
)

type TcpServer struct {
  Server   interface{}
  Addr     string
  Listener net.Listener
}

func (t *TcpServer) ListenAndServe() {
  reflect.ValueOf(t.Server).MethodByName("Serve").Call([]reflect.Value{
    reflect.ValueOf(t.Listener),
  })
}
func (t *TcpServer) Shutdown(ctx context.Context) {
  reflect.ValueOf(t.Server).MethodByName("Shutdown").Call([]reflect.Value{
    reflect.ValueOf(ctx),
  })
}

//func (t *TcpServer) Addr() string {
//  return reflect.ValueOf(t.Server).Elem().FieldByName("Addr").String()
//}
func Gen(server interface{}) *TcpServer {
  //t = &TcpServer{
  //  Server: server,
  //  Addr:   reflect.ValueOf(server).Elem().FieldByName("Addr").String(),
  //}
  //t.Listener, err = net.Listen("tcp", t.Addr)
  return &TcpServer{
    Server: server,
    Addr:   reflect.ValueOf(server).Elem().FieldByName("Addr").String(),
  }
}
