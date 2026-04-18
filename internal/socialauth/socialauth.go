// Package socialauth verifies identity tokens from Apple and Google
// sign-in flows. Both providers issue JWTs; this package fetches the
// JWKS public keys, validates the signature + claims, and returns the
// authenticated identity (provider, sub, email).
//
// Usage:
//
//	v := socialauth.New("me.pingclaw.app", "xxxx.apps.googleusercontent.com")
//	id, err := v.Verify(ctx, socialauth.ProviderApple, idTokenJWT)
//	// id.Sub is the stable user identifier from the provider
package socialauth

import (
	"context"
	"fmt"
)

// Provider identifies the social sign-in source.
type Provider string

const (
	ProviderApple  Provider = "apple"
	ProviderGoogle Provider = "google"
)

// Identity is the verified result of a social sign-in. Sub is the
// provider's stable, opaque user identifier (the JWT "sub" claim).
// Email may be empty — Apple lets users hide it.
type Identity struct {
	Provider Provider
	Sub      string
	Email    string
}

// Verifier validates identity tokens from Apple and Google.
type Verifier struct {
	apple  *appleVerifier
	google *googleVerifier
}

// New creates a Verifier. Both Apple and Google tokens may arrive from
// different platforms (iOS vs web) with different audience claims, so
// both accept multiple valid audiences.
func New(appleAudiences []string, googleAudiences []string) *Verifier {
	return &Verifier{
		apple:  newAppleVerifier(appleAudiences),
		google: newGoogleVerifier(googleAudiences),
	}
}

// Verify validates the raw JWT id_token for the given provider and
// returns the verified identity. Returns an error if the token is
// invalid, expired, or has the wrong audience.
func (v *Verifier) Verify(ctx context.Context, provider Provider, idToken string) (*Identity, error) {
	switch provider {
	case ProviderApple:
		return v.apple.verify(ctx, idToken)
	case ProviderGoogle:
		return v.google.verify(ctx, idToken)
	default:
		return nil, fmt.Errorf("unsupported provider: %q", provider)
	}
}
