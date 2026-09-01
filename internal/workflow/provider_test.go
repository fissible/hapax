package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/mode"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/workflow"
)

// Which provider a run gets is decided here, once, from a resolved mode and a
// requested choice. The two construction arms are supplied by cmd/hapax, which
// is the only place allowed to read the environment — so the cloud arm closes
// over the credential lookup and this package never sees a key.
//
// The proof that local-only cannot reach a credential is that the cloud ARM IS
// NEVER CALLED. Counting credential reads is not enough on its own, and that is
// not a hypothetical: cloud construction does not read a credential either, it
// defers that to the request. A test watching only credential reads would pass
// against a resolver that built an Anthropic provider under --local-only and
// simply had not used it yet.

type factorySpy struct {
	local, cloud int
	choices      []workflow.ProviderChoice
	err          error
}

func (s *factorySpy) factory() workflow.ProviderFactory {
	return workflow.ProviderFactory{
		Local: func(choice workflow.ProviderChoice) (rewrite.Provider, error) {
			s.local++
			s.choices = append(s.choices, choice)
			return stubProvider{}, s.err
		},
		Cloud: func(choice workflow.ProviderChoice) (rewrite.Provider, error) {
			s.cloud++
			s.choices = append(s.choices, choice)
			return stubProvider{}, s.err
		},
	}
}

type stubProvider struct{}

func (stubProvider) Rewrite(context.Context, rewrite.RewriteRequest) (string, error) {
	return "", errors.New("this provider exists to be counted, not called")
}

// Exactly one arm runs, and it is the one the choice names.
func TestTheResolverRunsExactlyOneArm(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name         string
		provider     string
		local, cloud int
	}{
		{"ollama", string(llm.ProviderOllama), 1, 0},
		{"anthropic", string(llm.ProviderAnthropic), 0, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			spy := &factorySpy{}
			runner := workflow.Default()
			runner.Providers = spy.factory()

			provider, err := runner.Provider(mode.Mode{}, workflow.ProviderChoice{
				Provider: c.provider, Model: "a-model",
			})
			if err != nil {
				t.Fatalf("Provider: %v", err)
			}
			if provider == nil {
				t.Fatal("resolved no provider and no error")
			}
			if spy.local != c.local || spy.cloud != c.cloud {
				t.Errorf("local arm ran %d times and cloud %d, want %d and %d",
					spy.local, spy.cloud, c.local, c.cloud)
			}
		})
	}
}

// Local-only refuses the cloud provider, and refuses it BEFORE the arm runs.
// This is the guarantee DESIGN calls tested rather than documented, and the
// zero on the cloud arm is what tests it.
func TestLocalOnlyRefusesTheCloudArmWithoutRunningIt(t *testing.T) {
	t.Parallel()
	spy := &factorySpy{}
	runner := workflow.Default()
	runner.Providers = spy.factory()

	_, err := runner.Provider(mode.Mode{LocalOnly: true}, workflow.ProviderChoice{
		Provider: string(llm.ProviderAnthropic), Model: "claude-sonnet-5",
	})

	if !errors.Is(err, workflow.ErrLocalOnlyForbidsProvider) {
		t.Fatalf("error = %v, want ErrLocalOnlyForbidsProvider", err)
	}
	if spy.cloud != 0 {
		t.Errorf("the cloud arm ran %d times under local-only", spy.cloud)
	}
	if spy.local != 0 {
		t.Errorf("the local arm ran %d times for a refused cloud choice", spy.local)
	}
}

// And local-only still resolves a local provider: it forbids the cloud, not
// rewriting.
func TestLocalOnlyStillResolvesTheLocalArm(t *testing.T) {
	t.Parallel()
	spy := &factorySpy{}
	runner := workflow.Default()
	runner.Providers = spy.factory()

	if _, err := runner.Provider(mode.Mode{LocalOnly: true}, workflow.ProviderChoice{
		Provider: string(llm.ProviderOllama), Model: "llama3",
	}); err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if spy.local != 1 || spy.cloud != 0 {
		t.Errorf("local arm ran %d times and cloud %d, want 1 and 0", spy.local, spy.cloud)
	}
}

// An unknown choice is refused before either arm runs. The old test for this
// set Config.Provider = "gpt"; there is no such field now, so the property moved
// up here rather than disappearing with the field that expressed it.
func TestAnUnknownProviderIsRefusedBeforeEitherArmRuns(t *testing.T) {
	t.Parallel()
	spy := &factorySpy{}
	runner := workflow.Default()
	runner.Providers = spy.factory()

	_, err := runner.Provider(mode.Mode{}, workflow.ProviderChoice{Provider: "gpt", Model: "gpt-5"})
	if !errors.Is(err, workflow.ErrUnknownProvider) {
		t.Fatalf("error = %v, want ErrUnknownProvider", err)
	}
	if spy.local != 0 || spy.cloud != 0 {
		t.Errorf("an unknown provider ran the local arm %d times and the cloud arm %d",
			spy.local, spy.cloud)
	}
}

// A Runner built without the binary has no arms at all, because workflow.Default
// leaves them nil: the environment is cmd/hapax's to supply and a library
// constructing a cloud provider on its own would be a second boundary.
func TestARunnerWithNoFactoryRefusesRatherThanImprovises(t *testing.T) {
	t.Parallel()
	_, err := workflow.Default().Provider(mode.Mode{}, workflow.ProviderChoice{
		Provider: string(llm.ProviderOllama), Model: "llama3",
	})
	if !errors.Is(err, workflow.ErrNoProviderFactory) {
		t.Errorf("error = %v, want ErrNoProviderFactory", err)
	}
}

// One arm present and the other absent is the interesting case: a resolver that
// fell through to whichever arm it had would send prose to a provider nobody
// chose, and a resolver that dereferenced the nil one would panic. Both arms are
// checked, because supplying only the local one is the plausible mistake and
// supplying only the cloud one is the dangerous one.
func TestASelectedArmThatIsAbsentIsRefusedRatherThanSubstituted(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		provider string
		present  string
	}{
		{"cloud selected, only local supplied", string(llm.ProviderAnthropic), "local"},
		{"local selected, only cloud supplied", string(llm.ProviderOllama), "cloud"},
	} {
		t.Run(c.name, func(t *testing.T) {
			spy := &factorySpy{}
			factory := spy.factory()
			if c.present == "local" {
				factory.Cloud = nil
			} else {
				factory.Local = nil
			}
			runner := workflow.Default()
			runner.Providers = factory

			_, err := runner.Provider(mode.Mode{}, workflow.ProviderChoice{
				Provider: c.provider, Model: "a-model",
			})
			if !errors.Is(err, workflow.ErrNoProviderFactory) {
				t.Fatalf("error = %v, want ErrNoProviderFactory", err)
			}
			if spy.local != 0 || spy.cloud != 0 {
				t.Errorf("the arm that was present ran anyway: local %d, cloud %d",
					spy.local, spy.cloud)
			}
		})
	}
}

// The choice reaches the arm intact. A resolver that picked the right arm and
// handed it the wrong model would satisfy every count above.
func TestTheChoiceReachesTheArmItSelected(t *testing.T) {
	t.Parallel()
	for _, want := range []workflow.ProviderChoice{
		{Provider: string(llm.ProviderOllama), Model: "llama3", Endpoint: "http://127.0.0.1:9999"},
		{Provider: string(llm.ProviderAnthropic), Model: "claude-sonnet-5"},
	} {
		t.Run(want.Provider, func(t *testing.T) {
			spy := &factorySpy{}
			runner := workflow.Default()
			runner.Providers = spy.factory()

			if _, err := runner.Provider(mode.Mode{}, want); err != nil {
				t.Fatalf("Provider: %v", err)
			}
			if len(spy.choices) != 1 {
				t.Fatalf("%d choices reached an arm, want 1", len(spy.choices))
			}
			if spy.choices[0] != want {
				t.Errorf("the arm received %+v, want %+v", spy.choices[0], want)
			}
		})
	}
}

// A failing arm is the caller's error, not a reason to try the other one. A
// cloud failure that fell back to local would send prose to a different provider
// than the one the user chose.
func TestAFailingArmIsNotAReasonToTryTheOther(t *testing.T) {
	t.Parallel()
	// Both directions. A cloud failure falling back to local would send the
	// author's prose to a provider they did not choose, which is the worse of
	// the two and was the one this test did not cover.
	for _, c := range []struct {
		name                 string
		provider             string
		wantLocal, wantCloud int
	}{
		{"local fails", string(llm.ProviderOllama), 1, 0},
		{"cloud fails", string(llm.ProviderAnthropic), 0, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			refused := errors.New("this arm cannot be constructed")
			spy := &factorySpy{err: refused}
			runner := workflow.Default()
			runner.Providers = spy.factory()

			if _, err := runner.Provider(mode.Mode{}, workflow.ProviderChoice{
				Provider: c.provider, Model: "a-model",
			}); !errors.Is(err, refused) {
				t.Fatalf("error = %v, want the arm's own failure", err)
			}
			if spy.local != c.wantLocal || spy.cloud != c.wantCloud {
				t.Errorf("local arm ran %d times and cloud %d, want %d and %d",
					spy.local, spy.cloud, c.wantLocal, c.wantCloud)
			}
		})
	}
}
