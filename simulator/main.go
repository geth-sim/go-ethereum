package main

import (
	"fmt"

	"github.com/ethereum/go-ethereum/simulator/evmsim"
)

func main() {
	fmt.Println("run main.go")

	evmsim.StartEvmSimulator()
	// triesim.StartTrieSimulator()
}
