package main

import (
	"net"

	"gitee.com/garryz/hive-go/rpc"
)

type Args struct {
	A int
	B int
}

type Arith int

func (t *Arith) Add(args Args, reply *int) error {
	*reply = args.A + args.B
	return nil
}

func (t *Arith) Print(s string) error {
	println("cluster_service", s)
	return nil
}

func main() {
	// client, err := rpc.Dial("tcp", "127.0.0.1:10001")
	// if err != nil {
	// 	panic(err)
	// }

	// client.Send("cluster_service", "print", "测试cluster")

	// var reply int
	// err = client.Call("cluster_service", "add", args{1, 2}, &reply)
	// if err != nil {
	// 	panic(err)
	// }

	// fmt.Printf("%#v\n", reply)
	// client.Close()

	rpc.RegisterName("cluster_service", new(Arith))

	l, e := net.Listen("tcp", "127.0.0.1:10001")
	if e != nil {
		panic(e)
	}
	rpc.Accept(l)
}
