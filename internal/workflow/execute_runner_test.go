package workflow_test

// The seams Execute is driven through: the two provider arms, a fixed invocation
// identity so an attempt can be read back by name, and the request/result
// helpers. Everything the fixture itself needs — the corpus, the release, and
// the oracle that checks a candidate is acceptable before a test relies on it —
// is in execute_fixture_test.go.

import (
	"testing"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// Runners
// ---------------------------------------------------------------------------

// arm records every choice that reached it, so a test can assert which arm ran
// as well as what it was given.
type arm struct {
	provider rewrite.Provider
	err      error
	choices  []workflow.ProviderChoice
}

func (a *arm) build(choice workflow.ProviderChoice) (rewrite.Provider, error) {
	a.choices = append(a.choices, choice)
	if a.err != nil {
		return nil, a.err
	}
	return a.provider, nil
}

// executingRunner is a runner with both arms installed and a fixed invocation
// identity, so an attempt can be read back out of the store by name.
func executingRunner(local, cloud *arm) (*workflow.Runner, string) {
	runner := workflow.Default()
	const invocation = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	runner.NewInvocationID = func() (string, error) { return invocation, nil }
	if local != nil {
		runner.Providers.Local = local.build
	}
	if cloud != nil {
		runner.Providers.Cloud = cloud.build
	}
	return runner, invocation
}

func localChoice() workflow.ProviderChoice {
	return workflow.ProviderChoice{Provider: "ollama", Model: "llama3"}
}

func cloudChoice() workflow.ProviderChoice {
	return workflow.ProviderChoice{Provider: "anthropic", Model: "claude-sonnet-5"}
}

func executeRequest(plan workflow.RewritePlan, choice workflow.ProviderChoice) workflow.ExecuteRequest {
	return workflow.ExecuteRequest{Plan: plan, Choice: choice}
}

func executed(t *testing.T, runner *workflow.Runner, request workflow.ExecuteRequest) workflow.ExecuteResult {
	t.Helper()
	result, err := runner.Execute(ctx(), request)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return result
}
