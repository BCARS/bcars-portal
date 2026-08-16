package main

import (
	"net/url"
	"strings"
)

// cookieReachabilityWarning reports the one configuration that breaks sign-in
// while looking like nothing is wrong, or "" when the configuration is sound.
//
// # THE FAILURE THIS EXISTS FOR
//
// Session cookies carry Secure by default, and a browser will accept a Secure
// cookie over a plaintext connection and then refuse to send it back. So
// signing in appears to work — the credentials are right, the server sets the
// cookie, the browser stores it — and the very next request arrives with no
// cookie at all. The portal bounces to the sign-in page, correctly, because as
// far as it can tell nobody is signed in.
//
// Nothing is logged by that sequence, because nothing went wrong in it. Every
// component behaved exactly as designed. The result is a login loop with no
// error anywhere to search for, which is why it was worth a startup check
// rather than a line in the documentation (bcars-portal-fmc.22).
//
// # WHAT IS NOT WARNED ABOUT
//
// A https base URL is the production shape and needs nothing said. Neither does
// -allow-insecure-cookies, which is the operator having already made this
// decision: warning them about a choice they just made teaches them to ignore
// warnings.
func cookieReachabilityWarning(baseURL string, allowInsecureCookies bool) string {
	if allowInsecureCookies {
		return ""
	}

	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme != "http" {
		// Anything that is not plainly http — https, or a value this cannot
		// read — is not the failure described above. An unusable base URL is a
		// separate problem and is not diagnosed by guessing here.
		return ""
	}

	return "sign-in will not work at this base URL: it is plaintext http, and session " +
		"cookies are Secure, so a browser will accept the session cookie and then refuse " +
		"to send it back, which looks like a correct password bouncing straight back to " +
		"the sign-in page. Serve the portal over https (a TLS-terminating proxy in front " +
		"is enough, with -base-url naming the https address), or pass " +
		"-allow-insecure-cookies for local development."
}
