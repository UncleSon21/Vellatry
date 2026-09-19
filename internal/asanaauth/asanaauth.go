// Package asanaauth holds the Asana OAuth settings. It only builds URLs, so the api role
// may use it to start a connection without ever calling Asana.
package asanaauth

import "golang.org/x/oauth2"

// Endpoint is Asana's OAuth endpoint.
var Endpoint = oauth2.Endpoint{
	AuthURL:   "https://app.asana.com/-/oauth_authorize",
	TokenURL:  "https://app.asana.com/-/oauth_token",
	AuthStyle: oauth2.AuthStyleInParams,
}

// RevokeURL revokes a refresh token.
const RevokeURL = "https://app.asana.com/-/oauth_revoke"

// Config returns the OAuth config for Vellatry's Asana app.
func Config(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL, Endpoint: Endpoint}
}

// ConsentURL is where the browser goes to approve the connection.
func ConsentURL(c *oauth2.Config, state string) string {
	return c.AuthCodeURL(state)
}
