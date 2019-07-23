package main

import (
	"fmt"
	"net"
	"reflect"
)

func main() {
	a, err := net.Listen("unix", "/tmp/echo.sock")
	//check(a)
	fmt.Println(reflect.TypeOf(a))
	fmt.Println(a.Addr())
	//b, err := net.ResolveTCPAddr("unix", a.Addr())
	//fmt.Println(b)
	fmt.Println(err)

}
func check(a ...interface{}) {
	for _, i := range a {
		fmt.Println(i)
		fmt.Println(reflect.TypeOf(i).String())
		if reflect.TypeOf(i).String() == "*http.Server" {
			fmt.Print("aaa")
		}

	}
	if reflect.TypeOf(a).String() == "*http.Server" {
		fmt.Print("aaa")
	}
}
