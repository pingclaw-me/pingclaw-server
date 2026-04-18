package socialauth

import (
	"context"
	"fmt"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

const googleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

// Google issues tokens with either of these issuers.
var googleIssuers = []string{
	"https://accounts.google.com",
	"accounts.google.com",
}

type googleVerifier struct {
	audiences []string
	jwks      keyfunc.Keyfunc
}

func newGoogleVerifier(audiences []string) *googleVerifier {
	jwks, err := keyfunc.NewDefault([]string{googleJWKSURL})
	if err != nil {
		return &googleVerifier{audiences: audiences}
	}
	return &googleVerifier{audiences: audiences, jwks: jwks}
}

func (g *googleVerifier) verify(_ context.Context, idToken string) (*Identity, error) {
	if g.jwks == nil {
		return nil, fmt.Errorf("google JWKS not initialised")
	}

	// Try each known audience × each known issuer.
	var token *jwt.Token
	var parseErr error
	for _, aud := range g.audiences {
		if aud == "" {
			continue
		}
		for _, iss := range googleIssuers {
			token, parseErr = jwt.Parse(idToken, g.jwks.KeyfuncCtx(context.Background()),
				jwt.WithIssuer(iss),
				jwt.WithAudience(aud),
				jwt.WithValidMethods([]string{"RS256"}),
			)
			if parseErr == nil {
				break
			}
		}
		if parseErr == nil {
			break
		}
	}
	if parseErr != nil {
		return nil, fmt.Errorf("google token invalid: %w", parseErr)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("google token: unexpected claims type")
	}

	sub, _ := claims.GetSubject()
	if sub == "" {
		return nil, fmt.Errorf("google token: missing sub claim")
	}

	return &Identity{
		Provider: ProviderGoogle,
		Sub:      sub,
	}, nil
}
