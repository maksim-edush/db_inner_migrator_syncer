package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"db_inner_migrator_syncer/internal/config"
)

const googleIssuer = "https://accounts.google.com"

type OIDCProvider struct {
	oauthConfig    *oauth2.Config
	verifier       *oidc.IDTokenVerifier
	allowedDomains map[string]struct{}
}

type IDTokenClaims struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	Name          string `json:"name"`
	EmailVerified bool   `json:"email_verified"`
	HostedDomain  string `json:"hd"`
	Nonce         string `json:"nonce"`
}

func NewOIDCProvider(ctx context.Context, cfg config.Config) (*OIDCProvider, error) {
	provider, err := oidc.NewProvider(ctx, googleIssuer)
	if err != nil {
		return nil, fmt.Errorf("create oidc provider: %w", err)
	}

	verifier := provider.Verifier(&oidc.Config{
		ClientID: cfg.OIDC.ClientID,
	})

	oauthCfg := &oauth2.Config{
		ClientID:     cfg.OIDC.ClientID,
		ClientSecret: cfg.OIDC.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.OIDC.RedirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}

	allowed := make(map[string]struct{})
	for _, d := range cfg.OIDC.AllowedDomains {
		allowed[strings.ToLower(d)] = struct{}{}
	}

	return &OIDCProvider{
		oauthConfig:    oauthCfg,
		verifier:       verifier,
		allowedDomains: allowed,
	}, nil
}

func (p *OIDCProvider) AuthCodeURL(state, nonce string) string {
	return p.oauthConfig.AuthCodeURL(state, oidc.Nonce(nonce))
}

func (p *OIDCProvider) Exchange(ctx context.Context, code string, expectedNonce string) (*IDTokenClaims, error) {
	oauth2Token, err := p.oauthConfig.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}

	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("missing id_token in response")
	}

	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("verify id token: %w", err)
	}

	var claims IDTokenClaims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	if expectedNonce != "" && claims.Nonce != expectedNonce {
		return nil, fmt.Errorf("nonce mismatch")
	}

	if len(p.allowedDomains) > 0 {
		domain := emailDomain(claims.Email)
		if _, ok := p.allowedDomains[domain]; !ok {
			return nil, fmt.Errorf("email domain not allowed")
		}
	}

	return &claims, nil
}

func emailDomain(email string) string {
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return ""
	}
	return strings.ToLower(parts[1])
}
