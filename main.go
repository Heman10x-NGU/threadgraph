package main

import (
	"fmt"
	"os"

	"github.com/Heman10x-NGU/threadgraph/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
