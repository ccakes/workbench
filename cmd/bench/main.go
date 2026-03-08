package main

import (
	"os"

	"github.com/ccakes/workbench/internal/cli"
)

func main() {
	os.Exit(cli.Run())
}
