package udpdown

import "net"

type UdpServer struct {
  Addr    string
  Network string
  Threads int
  Data    interface{}
  Handler func(*net.UDPConn, interface{})
}
