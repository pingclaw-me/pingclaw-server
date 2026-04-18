package socialauth

import (
	"context"
	"fmt"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

const appleJWKSURL = "https://appleid.apple.com/auth/keys"
const appleIssuer  = "https://appleid.apple.com"

type appleVerifier struct {
	audiences []string // iOS bundle ID + web Services ID
	jwks      keyfunc.Keyfunc
}

func newAppleVerifier(audiences []string) *appleVerifier {
	jwks, err := keyfunc.NewDefault([]string{appleJWKSURL})
	if err != nil {
		return &appleVerifier{audiences: audiences}
	}
	return &appleVerifier{audiences: audiences, jwks: jwks}
}

func (a *appleVerifier) verify(_ context.Context, idToken string) (*Identity, error) {
	if a.jwks == nil {
		return nil, fmt.Errorf("apple JWKS not initialised")
	}

	// Try each known audience (iOS bundle ID, web Services ID).
	var token *jwt.Token
	var parseErr error
	for _, aud := range a.audiences {
		if aud == "" {
			continue
		}
		token, parseErr = jwt.Parse(idToken, a.jwks.KeyfuncCtx(context.Background()),
			jwt.WithIssuer(appleIssuer),
			jwt.WithAudience(aud),
			jwt.WithValidMethods([]string{"RS256", "ES256"}),
		)
		if parseErr == nil {
			break
		}
	}
	if parseErr != nil {
		return nil, fmt.Errorf("apple token invalid: %w", parseErr)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("apple token: unexpected claims type")
	}

	sub, _ := claims.GetSubject()
	if sub == "" {
		return nil, fmt.Errorf("apple token: missing sub claim")
	}

	return &Identity{
		Provider: ProviderApple,
		Sub:      sub,
	}, nil
}
