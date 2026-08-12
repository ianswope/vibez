package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// statusServer serves a fixed status code and records the request it saw.
func statusServer(t *testing.T, code int) (*httptest.Server, *http.Header) {
	t.Helper()
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// deadURL points at a port that refuses connections.
const deadURL = "http://127.0.0.1:1"

func TestCheckTokens_BothValid(t *testing.T) {
	user, _ := statusServer(t, http.StatusOK)
	dev, _ := statusServer(t, http.StatusOK)

	if got := checkTokens(user.URL, dev.URL, "dev", "user"); got != TokensValid {
		t.Errorf("checkTokens = %v, want TokensValid", got)
	}
}

func TestCheckTokens_UserTokenExpired(t *testing.T) {
	// The combined request is rejected but the developer token alone works,
	// which isolates the Music User Token as the expired credential.
	user, _ := statusServer(t, http.StatusUnauthorized)
	dev, _ := statusServer(t, http.StatusOK)

	if got := checkTokens(user.URL, dev.URL, "dev", "user"); got != UserTokenRejected {
		t.Errorf("checkTokens = %v, want UserTokenRejected", got)
	}
}

// TestCheckTokens_DeveloperTokenExpired is the regression test for the bug
// where an expired developer token was reported as an expired session, and the
// caller then cleared a perfectly good Music User Token.
func TestCheckTokens_DeveloperTokenExpired(t *testing.T) {
	user, _ := statusServer(t, http.StatusUnauthorized)
	dev, _ := statusServer(t, http.StatusUnauthorized)

	if got := checkTokens(user.URL, dev.URL, "dev", "user"); got != DeveloperTokenRejected {
		t.Errorf("checkTokens = %v, want DeveloperTokenRejected", got)
	}
}

func TestCheckTokens_ForbiddenIsRejection(t *testing.T) {
	user, _ := statusServer(t, http.StatusForbidden)
	dev, _ := statusServer(t, http.StatusForbidden)

	if got := checkTokens(user.URL, dev.URL, "dev", "user"); got != DeveloperTokenRejected {
		t.Errorf("checkTokens = %v, want DeveloperTokenRejected", got)
	}
}

func TestCheckTokens_MissingCredentials(t *testing.T) {
	if got := CheckTokens("", "user"); got != DeveloperTokenRejected {
		t.Errorf("empty devToken = %v, want DeveloperTokenRejected", got)
	}
	if got := CheckTokens("dev", ""); got != UserTokenRejected {
		t.Errorf("empty userToken = %v, want UserTokenRejected", got)
	}
	if got := CheckTokens("", ""); got != DeveloperTokenRejected {
		t.Errorf("both empty = %v, want DeveloperTokenRejected", got)
	}
}

func TestCheckTokens_NetworkErrorKeepsSession(t *testing.T) {
	dev, _ := statusServer(t, http.StatusOK)

	if got := checkTokens(deadURL, dev.URL, "dev", "user"); got != TokensValid {
		t.Errorf("checkTokens = %v, want TokensValid on network error", got)
	}
}

// A rejection the tiebreaker cannot attribute must not cost the user a session.
func TestCheckTokens_InconclusiveTiebreakerKeepsSession(t *testing.T) {
	user, _ := statusServer(t, http.StatusUnauthorized)

	if got := checkTokens(user.URL, deadURL, "dev", "user"); got != TokensValid {
		t.Errorf("checkTokens = %v, want TokensValid on inconclusive tiebreaker", got)
	}
}

func TestCheckTokens_SendsBothCredentials(t *testing.T) {
	user, userHeaders := statusServer(t, http.StatusOK)
	dev, _ := statusServer(t, http.StatusOK)

	checkTokens(user.URL, dev.URL, "mydev", "myuser")

	if got := userHeaders.Get("Authorization"); got != "Bearer mydev" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer mydev")
	}
	if got := userHeaders.Get("Music-User-Token"); got != "myuser" {
		t.Errorf("Music-User-Token = %q, want %q", got, "myuser")
	}
}

// The tiebreaker must carry the developer token alone, or it proves nothing.
func TestCheckTokens_TiebreakerOmitsUserToken(t *testing.T) {
	user, _ := statusServer(t, http.StatusUnauthorized)
	dev, devHeaders := statusServer(t, http.StatusOK)

	checkTokens(user.URL, dev.URL, "mydev", "myuser")

	if got := devHeaders.Get("Authorization"); got != "Bearer mydev" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer mydev")
	}
	if got := devHeaders.Get("Music-User-Token"); got != "" {
		t.Errorf("Music-User-Token = %q, want it unset", got)
	}
}
