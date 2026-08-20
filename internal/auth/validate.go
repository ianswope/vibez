package auth

import (
	"context"
	"net/http"
	"time"
)

const (
	// storefrontURL needs both a developer token and a Music User Token, so a
	// rejection here does not say which of the two Apple objected to.
	storefrontURL = "https://api.music.apple.com/v1/me/storefront"
	// catalogURL needs only a developer token, which makes it the tiebreaker
	// when storefrontURL rejects a request.
	catalogURL = "https://api.music.apple.com/v1/storefronts"
)

// TokenStatus reports which credential Apple Music rejected.
type TokenStatus int

const (
	// TokensValid means both credentials were accepted, or that the check
	// could not be completed and the caller should carry on as before.
	TokensValid TokenStatus = iota
	// UserTokenRejected means the Music User Token is expired, revoked or
	// absent while the developer token still works. Logging in again fixes it.
	UserTokenRejected
	// DeveloperTokenRejected means the developer token is expired, revoked or
	// absent. Logging in again cannot fix it, because MusicKit needs a valid
	// developer token before it will ask the user to authorize anything.
	DeveloperTokenRejected
)

// DeveloperTokenStatus is a one-line summary for a status bar.
const DeveloperTokenStatus = "Developer token rejected - update vibez to continue"

// DeveloperTokenHelp is the same failure explained where there is room for it.
//
//nolint:gosec // G101: prose naming a config key, not a credential
const DeveloperTokenHelp = `Apple Music rejected vibez's developer token, which is a problem with this
build rather than with your account. Your saved session has been kept.
Updating to a newer release restores access; a source build can instead set
apple_developer_token in the config.`

// CheckTokens asks Apple Music which of the stored credentials still works.
//
// It is deliberately conservative: anything it cannot establish comes back as
// TokensValid, so a flaky network never costs the user a working session.
func CheckTokens(devToken, userToken string) TokenStatus {
	return checkTokens(storefrontURL, catalogURL, devToken, userToken)
}

// checkTokens is the testable core of CheckTokens. It takes both URLs
// explicitly so tests can point them at an httptest server.
func checkTokens(userURL, devURL, devToken, userToken string) TokenStatus {
	if devToken == "" {
		return DeveloperTokenRejected
	}
	if userToken == "" {
		return UserTokenRejected
	}

	if probe(userURL, devToken, userToken) != probeRejected {
		return TokensValid
	}

	// Both credentials travelled together on that request, so the rejection is
	// still unattributed. A developer-token-only endpoint settles it.
	switch probe(devURL, devToken, "") {
	case probeRejected:
		return DeveloperTokenRejected
	case probeAccepted:
		return UserTokenRejected
	default:
		// The tiebreaker did not complete, so the cause stays unknown. Keep
		// the user token rather than discarding it on a guess.
		return TokensValid
	}
}

// probeResult is the outcome of a single credential probe.
type probeResult int

const (
	probeAccepted probeResult = iota
	probeRejected
	// probeInconclusive covers network failures and any response that is
	// neither a clear acceptance nor a clear rejection, where assuming the
	// worst would force an unnecessary re-login.
	probeInconclusive
)

// probe makes one lightweight Apple Music request. An empty userToken sends
// the developer token alone.
func probe(url, devToken, userToken string) probeResult {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // URL is a hardcoded constant or test server
	if err != nil {
		return probeInconclusive
	}
	req.Header.Set("Authorization", "Bearer "+devToken)
	if userToken != "" {
		req.Header.Set("Music-User-Token", userToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return probeInconclusive
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return probeRejected
	}
	// Only a 2xx proves the credentials were accepted. A 429 or a 5xx says
	// nothing about them, and treating it as acceptance would let a transient
	// Apple outage attribute the rejection to the user token and wipe it.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return probeAccepted
	}
	return probeInconclusive
}
