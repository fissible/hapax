package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/fissible/hapax/internal/cli"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/publish"
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
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		Env:       os.LookupEnv,
		Now:       time.Now,
		ReadFile:  os.ReadFile,
		Getwd:     os.Getwd,
		Service:   runner,
		Publisher: publisher{},
	}))
}

// publisher is the composition root's adapter over internal/publish. It lives
// here rather than in cli because cli's own import guard forbids reaching a
// filesystem package directly — the seam is an interface, and wiring the real
// one is this file's job.
type publisher struct{}

func (publisher) Create(source, destination string, content []byte) error {
	return translate(publish.Create(source, destination, content))
}
func (publisher) Replace(source string, content []byte) error {
	return translate(publish.Replace(source, content))
}

// translate maps publish's refusals into cli's, so cli can classify them without
// naming — and therefore without being able to call — the package that writes.
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, publish.ErrExists):
		return fmt.Errorf("%w: %v", cli.ErrDestinationExists, err)
	case errors.Is(err, publish.ErrAliasesInput):
		return fmt.Errorf("%w: %v", cli.ErrDestinationIsInput, err)
	default:
		return err
	}
}
