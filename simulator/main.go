package main

import (
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/simulator/statesim"
)

func main() {
	fmt.Println("run main.go")

	if len(os.Args) > 1 {
		args := os.Args[1:]
		statesim.ServerPort = args[0]
		fmt.Println("set new server port:", statesim.ServerPort)
	} else {
		fmt.Println("using default server port:", statesim.ServerPort)
	}
	statesim.SetDbPath("")

	statesim.StartStateSimulator()
	// triesim.StartTrieSimulator()
}
