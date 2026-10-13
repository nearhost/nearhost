package main

import (
	"os"

	"github.com/nearhost/nearhost/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
