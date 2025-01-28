package main

import "net-pinger/src"

func main() {
	p, err := src.NewPingFromCommandLine()
	if err != nil {
		panic(err)
	}

	err = p.Run()
	if err != nil {
		panic(err)
	}
}
