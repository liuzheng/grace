// Package httpdown provides http.ConnState enabled graceful termination of
// http.Server.
package tcp

import (
	"context"
	"reflect"
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
func (t *TcpServer) Addr() string {
	return reflect.ValueOf(t.Server).FieldByName("Addr").String()
}
func Gen(server interface{}) *TcpServer {
	return &TcpServer{
		Server: server,
	}
}
