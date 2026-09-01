package main

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"github.com/fissible/hapax/internal/cli"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/workflow"
)

func main() {
	runner := workflow.Default()
	dial := (&net.Dialer{}).DialContext
	runner.Providers = workflow.ProviderFactory{
		Local: func(choice workflow.ProviderChoice) (rewrite.Provider, error) {
			cfg := llm.DefaultLocalConfig()
			cfg.Model = choice.Model
			if choice.Endpoint != "" {
				cfg.Endpoint = choice.Endpoint
			}
			return llm.NewLocal(cfg, dial, nil)
		},
		Cloud: func(choice workflow.ProviderChoice) (rewrite.Provider, error) {
			cfg := llm.DefaultCloudConfig()
			cfg.Model = choice.Model
			return llm.NewCloud(cfg, llm.CloudDeps{
				Dial: dial,
				Credentials: func(context.Context) (string, error) {
					key, ok := os.LookupEnv("ANTHROPIC_API_KEY")
					if !ok {
						return "", errors.New("ANTHROPIC_API_KEY is not set")
					}
					return key, nil
				},
			})
		},
	}
	os.Exit(cli.Run(context.Background(), os.Args[1:], cli.Deps{
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Env:      os.LookupEnv,
		Now:      time.Now,
		ReadFile: os.ReadFile,
		Getwd:    os.Getwd,
		Service:  runner,
	}))
}
