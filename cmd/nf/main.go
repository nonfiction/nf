package main

import (
	"os"

	"github.com/nonfiction/nf/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
