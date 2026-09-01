package llm_test

import (
	"context"
	"crypto/x509"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/rewrite"
)

// `--local-only` is a tested guarantee, and until this slice nothing exercised
// it: no command had ever constructed a provider.
//
// Two designs for the boundary were rejected before this one. Two types in one
// package, one holding a credential factory and one not, is not a boundary —
// both can name the type and both can call a constructor a later edit widens.
// Separate packages, the local one unable to *name* a credential type, is not
// one either, and that was checked rather than assumed:
//
//	package user            // imports cred, never names cred.Factory
//	func Build() cred.Deps {
//	    return cred.Deps{Credentials: func(context.Context) (string, error) {
//	        return "a secret this package should not supply", nil
//	    }}
//	}
//
// That compiles. A named function type accepts a matching literal, so an import
// allowlist would have passed while the boundary was absent.
//
// What is left is the signature. A local construction cannot supply a credential
// because there is nowhere to put one, at every call site, now and after any
// later edit. These tests are what stops the parameter growing back.

// The field inventories are written out here rather than derived from the types
// under test. A shape assertion generated from the implementation passes
// whatever the implementation is; these are the architectural allowlist, so
// returning Endpoint to CloudConfig — or admitting a credential to LocalConfig —
// fails here rather than in a review nobody runs.
func TestTheConfigurationsCarryExactlyTheseFields(t *testing.T) {
	for _, c := range []struct {
		name  string
		typed reflect.Type
		want  [][2]string
	}{
		{"LocalConfig", reflect.TypeOf(llm.LocalConfig{}), [][2]string{
			{"Model", "string"}, {"Endpoint", "string"}, {"Limits", "llm.Limits"},
		}},
		{"CloudConfig", reflect.TypeOf(llm.CloudConfig{}), [][2]string{
			{"Model", "string"}, {"Limits", "llm.Limits"}, {"MaxTokens", "int"},
		}},
		{"CloudDeps", reflect.TypeOf(llm.CloudDeps{}), [][2]string{
			{"Dial", "llm.DialFunc"}, {"Credentials", "llm.CredentialFactory"}, {"RootCAs", "*x509.CertPool"},
		}},
		{"Limits", reflect.TypeOf(llm.Limits{}), [][2]string{
			{"MaxRequestBytes", "int"}, {"MaxResponseBytes", "int"},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got [][2]string
			for i := 0; i < c.typed.NumField(); i++ {
				field := c.typed.Field(i)
				if !field.IsExported() {
					t.Errorf("%s.%s is unexported; this contract is the public shape", c.name, field.Name)
					continue
				}
				// The TYPE as well as the name. Names alone let Endpoint come
				// back as `any`, or Limits as something credential-bearing
				// behind an allowed name, and the allowlist would still pass.
				got = append(got, [2]string{field.Name, field.Type.String()})
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s is\n%v\nand the boundary declares\n%v", c.name, got, c.want)
			}
		})
	}
}

// The constructors' exact types. A widened NewLocal — one that grew a
// credential parameter, or took CloudDeps, or accepted a provider selector —
// stops satisfying this assignment and fails to compile, which is a stronger
// statement than any assertion this file could make at run time.
var (
	_ func(llm.LocalConfig, llm.DialFunc, *x509.CertPool) (rewrite.Provider, error) = llm.NewLocal
	_ func(llm.CloudConfig, llm.CloudDeps) (rewrite.Provider, error)                = llm.NewCloud
)

// The signatures are compile-time and the field types are structural, but
// neither can stop a NewLocal implementation reading a credential from package
// state or the environment and putting it on the wire. Only running it can.
//
// The earlier version of this test read the source for forbidden type names.
// That was a blocklist: it missed anything spelled differently, and it added
// nothing the assignment above does not already give. What is observable is the
// request the local path actually makes, so that is what this asserts.
func TestTheLocalPathSendsNoCredentialAndDialsOnlyLoopback(t *testing.T) {
	h := newHarness(t, plainServer, "127.0.0.1:11434")

	provider, err := llm.NewLocal(localConfig(loopback), h.dial, nil)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", true)); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}

	request, _ := h.onlyRequest(t)
	for _, header := range []string{"X-Api-Key", "Authorization", "Anthropic-Version", "Proxy-Authorization"} {
		if got := request.Header.Get(header); got != "" {
			t.Errorf("the local path sent %s: %q", header, got)
		}
	}
	// The URI as well as the headers. A secret travels just as well in
	// `?api_key=...` or in userinfo as in a header, and a body-and-header
	// inventory says nothing about either — so the target is asserted whole
	// rather than by listing the ways it could be wrong.
	if got := request.URL.RequestURI(); got != "/api/generate" {
		t.Errorf("the local path requested %q, want exactly %q", got, "/api/generate")
	}
	if request.URL.User != nil {
		t.Errorf("the local request carries userinfo: %q", request.URL.User)
	}
	// And the harness refuses any address but the ones it was given, all of
	// which are loopback, so a non-loopback dial fails rather than being
	// counted — h.dial is the boundary this relies on.
	if dials, requests, creds := h.counts(); dials != 1 || requests != 1 || creds != 0 {
		t.Errorf("dials=%d requests=%d credentialCalls=%d, want 1, 1 and 0", dials, requests, creds)
	}
}

// The credential type exists and is reachable only through the cloud
// dependencies. Named explicitly so that deleting it, or moving it onto the
// local path, is a visible change rather than a quiet one.
func TestTheCredentialFactoryIsCloudDepsOnly(t *testing.T) {
	deps := reflect.TypeOf(llm.CloudDeps{})
	field, ok := deps.FieldByName("Credentials")
	if !ok {
		t.Fatal("CloudDeps has no Credentials field")
	}
	if got := field.Type.String(); got != "llm.CredentialFactory" {
		t.Errorf("CloudDeps.Credentials is %s, want llm.CredentialFactory", got)
	}
	var factory llm.CredentialFactory = func(context.Context) (string, error) { return "", nil }
	if factory == nil {
		t.Fatal("unreachable; this pins the credential factory's own signature")
	}
}

// The exported surface, written out. This is the last channel the signature
// assignments and the field inventory leave open, and it is not hypothetical:
// a package-global setter — `SetLocalHook(func())`, a registry, a swappable
// default — invoked by NewLocal would keep every constructor signature and
// every config field exactly as declared, add no import, and leave the wire
// test clean until somebody registers something. Caller-supplied code could
// then read a credential or dial wherever it liked.
//
// So the package cannot grow an exported name without this failing. That is a
// blunt instrument and deliberately so: a new export here is a capability, and
// capabilities on the local path are what this slice exists to bound.
func TestTheExportedSurfaceIsExactlyThis(t *testing.T) {
	want := []string{
		// Sorted, and "(" sorts before letters, so the method entry leads.
		// The only exported method in the package: rewrite.Provider needs
		// exactly this one, and anything beside it — on any type here — is a
		// capability handed to whoever holds that value.
		"(method) provider.Rewrite",
		"AnthropicURL", "AnthropicVersion", "CloudConfig", "CloudDeps",
		"CredentialFactory", "DefaultCloudConfig", "DefaultEndpoint",
		"DefaultLimits", "DefaultLocalConfig", "DialFunc", "ErrEndpoint",
		"ErrInvalidConfig", "ErrLocalOnly", "ErrMissingInput", "ErrModeMismatch",
		"ErrProvider", "ErrRedirect", "ErrRequestTooLarge", "ErrResponseTooLarge",
		"Limits", "LocalConfig", "NewCloud", "NewLocal", "ProviderAnthropic",
		"ProviderID", "ProviderOllama", "Providers", "UserAgent",
	}

	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing internal/llm: %v", err)
	}
	var got []string
	scanned := 0
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			scanned++
			for _, declaration := range file.Decls {
				switch typed := declaration.(type) {
				case *ast.FuncDecl:
					if !typed.Name.IsExported() {
						continue
					}
					if typed.Recv == nil {
						got = append(got, typed.Name.Name)
						continue
					}
					// Exported METHODS count too, even on an unexported
					// receiver. An earlier version of this guard said they were
					// "reached through their receiver, which is already in this
					// list or is not exported at all", and that is false: a
					// caller holding the rewrite.Provider that NewLocal returns
					// can reach any exported method structurally.
					//
					//	p.(interface{ SetLocalHook(func()) }).SetLocalHook(hook)
					//
					// Keyed by receiver, and never compacted. Keying by name
					// alone let a second exported Rewrite — on ProviderID, say,
					// which Providers() hands out — appear as a duplicate that
					// slices.Compact then erased.
					got = append(got, "(method) "+typeOf(typed.Recv.List[0].Type)+"."+typed.Name.Name)
				case *ast.GenDecl:
					for _, spec := range typed.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if s.Name.IsExported() {
								got = append(got, s.Name.Name)
							}
						case *ast.ValueSpec:
							for _, name := range s.Names {
								if name.IsExported() {
									got = append(got, name.Name)
								}
							}
						}
					}
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no non-test source was scanned; this guard is vacuous")
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("internal/llm exports\n%v\nand the boundary declares\n%v", got, want)
	}
}

// The declaration scan above cannot see a method promoted from an embedded
// field: there is no FuncDecl for it in this package. What escapes is a value,
// so the value is what this asks.
//
// Both arms, because a capability added to one of them only is exactly the
// asymmetry this slice is about.
func TestAConstructedProviderExposesOnlyRewrite(t *testing.T) {
	h := newHarness(t, plainServer, "127.0.0.1:11434")
	local, err := llm.NewLocal(localConfig(loopback), h.dial, nil)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	cloud, err := llm.NewCloud(cloudConfig(), llm.CloudDeps{Dial: h.dial, Credentials: h.credentials})
	if err != nil {
		t.Fatalf("NewCloud: %v", err)
	}

	want := reflect.TypeOf((*rewrite.Provider)(nil)).Elem().Method(0).Type
	for _, c := range []struct {
		name  string
		value any
	}{{"local", local}, {"cloud", cloud}} {
		t.Run(c.name, func(t *testing.T) {
			typed := reflect.TypeOf(c.value)
			var methods []string
			for i := 0; i < typed.NumMethod(); i++ {
				methods = append(methods, typed.Method(i).Name)
			}
			if !reflect.DeepEqual(methods, []string{"Rewrite"}) {
				t.Fatalf("the %s provider exposes %v, want only Rewrite", c.name, methods)
			}
			// The exact signature, or "Rewrite" could be something else
			// entirely that happens to share a name.
			if got := typed.Method(0).Func.Type(); got.NumIn()-1 != want.NumIn() || got.NumOut() != want.NumOut() {
				t.Errorf("the %s provider's Rewrite is %v, want %v", c.name, got, want)
			}
		})
	}
}

func typeOf(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return typeOf(typed.X)
	case *ast.SelectorExpr:
		return typeOf(typed.X) + "." + typed.Sel.Name
	default:
		return "?"
	}
}
