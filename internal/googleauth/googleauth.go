// Package googleauth builds the Google OAuth configuration. It makes no network calls,
// so the api role may use it to build the consent URL; the code exchange and every
// data call happen in the worker (internal/google).
package googleauth

import (
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Scopes are read-only: Vellatry never changes a customer's Google data.
var Scopes = []string{
	"https://www.googleapis.com/auth/webmasters.readonly",
	"https://www.googleapis.com/auth/analytics.readonly",
	"openid",
	"email",
}

// Config returns the OAuth client configuration.
func Config(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       Scopes,
		Endpoint:     google.Endpoint,
	}
}

// ConsentURL is where the user grants access. Offline access with a forced consent
// screen guarantees a refresh token every time.
func ConsentURL(c *oauth2.Config, state string) string {
	return c.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce,
		oauth2.SetAuthURLParam("include_granted_scopes", "true"))
}
