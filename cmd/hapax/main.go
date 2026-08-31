package main

import (
	"context"
	"os"
	"time"

	"github.com/fissible/hapax/internal/cli"
	"github.com/fissible/hapax/internal/workflow"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], cli.Deps{
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Env:      os.LookupEnv,
		Now:      time.Now,
		ReadFile: os.ReadFile,
		Getwd:    os.Getwd,
		Service:  workflow.Default(),
	}))
}
