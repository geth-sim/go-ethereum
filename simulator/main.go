package main

import (
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/simulator/evmsim"
)

func main() {
	fmt.Println("run main.go")

	if len(os.Args) > 1 {
		args := os.Args[1:]
		evmsim.ServerPort = args[0]
		fmt.Println("set new server port:", evmsim.ServerPort)
	} else {
		fmt.Println("using default server port:", evmsim.ServerPort)
	}

	evmsim.StartEvmSimulator()
	// triesim.StartTrieSimulator()
}
