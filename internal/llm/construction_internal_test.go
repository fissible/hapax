package llm

import (
	"context"
	"crypto/x509"
	"net"
	"testing"
)

// The exported boundary is a signature, and the signature guard in shape_test.go
// is what holds it. This is the same guarantee one level down.
//
// The first implementation of that boundary passed every test while handing the
// shared constructor a `CloudDeps{Dial: dial, RootCAs: roots}` from the local
// path — a struct with a credential field, left nil. That is the design this
// slice rejected, reappearing internally: the local path had somewhere to put a
// credential and merely did not. A later edit setting that field would have kept
// the suite green, because the exported signature was unchanged and a
// correctly-written local path still sends nothing.
//
// Passing the credential as its own parameter and giving it nil is no better —
// the slot still exists. So the shared constructor takes transport only, and
// credential attachment belongs to the cloud path alone.
//
// In-package because the thing being pinned is unexported, and compile-time
// because that is what makes reintroducing the slot a build failure rather than
// a review catch.
// Returning the concrete type, not rewrite.Provider. The constructor is the
// package's own and always builds one thing, so erasing it there bought nothing
// and cost NewCloud an unchecked assertion — which would have panicked in a
// user's process the day anything else in this package implemented the
// interface.
var _ func(ProviderID, string, Limits, int, bool, string, DialFunc, *x509.CertPool) *provider = newProvider

// And the local path's provider genuinely holds no credential afterwards, which
// the signature alone cannot say: the constructor could take no credential and
// the cloud helper could still be applied to a local provider.
func TestALocallyConstructedProviderHoldsNoCredential(t *testing.T) {
	built, err := NewLocal(
		LocalConfig{Model: "llama3", Endpoint: DefaultEndpoint, Limits: DefaultLimits()},
		func(context.Context, string, string) (net.Conn, error) { return nil, nil },
		nil,
	)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	local, ok := built.(*provider)
	if !ok {
		t.Fatalf("NewLocal returned %T, and this test reads the concrete provider", built)
	}
	if local.credentials != nil {
		t.Error("a locally constructed provider carries a credential factory")
	}
	if !local.localOnly {
		t.Error("a locally constructed provider is not marked local")
	}
}
