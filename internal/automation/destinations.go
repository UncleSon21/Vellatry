package automation

import (
	"net/mail"
	"net/url"
	"strings"
)

// ValidSlackWebhook accepts only Slack incoming-webhook URLs. The worker posts to the
// stored URL, so anything else would let a customer aim our servers at an address of
// their choosing.
func ValidSlackWebhook(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host != "hooks.slack.com" || u.User != nil ||
		!strings.HasPrefix(u.Path, "/services/") || len(strings.Split(strings.Trim(u.Path, "/"), "/")) != 4 {
		return invalid("paste a Slack incoming webhook URL (https://hooks.slack.com/services/...)")
	}
	return nil
}

// MaxRecipients caps an email destination.
const MaxRecipients = 20

// ValidRecipients normalises an email destination's recipients. Each must be a member of
// the organisation or at the company's own domain (allowed), so Vellatry cannot be used
// to mail strangers.
func ValidRecipients(in []string, allowedDomains, members []string) ([]string, error) {
	if len(in) == 0 {
		return nil, invalid("add at least one recipient")
	}
	if len(in) > MaxRecipients {
		return nil, invalid("an email destination takes up to %d recipients", MaxRecipients)
	}
	member := map[string]bool{}
	for _, m := range members {
		member[strings.ToLower(m)] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range in {
		a, err := mail.ParseAddress(strings.TrimSpace(raw))
		if err != nil || strings.ContainsAny(a.Address, " ,;") {
			return nil, invalid("%q is not an email address", raw)
		}
		addr := strings.ToLower(a.Address)
		if seen[addr] {
			continue
		}
		seen[addr] = true
		if !member[addr] && !InDomains(addr, allowedDomains) {
			return nil, invalid("%s is not at your company's domain or a member of your team", addr)
		}
		out = append(out, addr)
	}
	return out, nil
}

// InDomains reports whether an address is at one of the domains or their subdomains.
func InDomains(addr string, domains []string) bool {
	at := strings.LastIndexByte(addr, '@')
	if at < 0 {
		return false
	}
	host := strings.ToLower(addr[at+1:])
	for _, d := range domains {
		d = strings.ToLower(strings.TrimPrefix(d, "www."))
		if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
			return true
		}
	}
	return false
}
