package site

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlockedAddress is returned when a crawl would connect to an address outside the
// public internet.
var ErrBlockedAddress = errors.New("refusing to crawl a private or reserved address")

// PublicOnlyClient returns an HTTP client that connects only to public addresses. The
// domain is typed in by a customer and the crawler runs inside our network, so without
// this a domain resolving to 169.254.169.254 or a private range would have us fetch our
// own infrastructure and show the result on their Site page. The check runs on the
// resolved address at connect time, so it also holds across redirects and DNS rebinding.
func PublicOnlyClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: refusePrivate}
	transport := &http.Transport{
		Proxy:                 nil, // a proxy would connect on our behalf and skip the check
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

func refusePrivate(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return ErrBlockedAddress
	}
	if !PublicAddr(ap.Addr()) {
		return ErrBlockedAddress
	}
	return nil
}

var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can reach IPv4 private ranges
	netip.MustParsePrefix("2001:db8::/32"),
}

// PublicAddr reports whether a is a routable public unicast address.
func PublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsUnspecified() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() || !a.IsGlobalUnicast() {
		return false
	}
	for _, p := range reserved {
		if p.Contains(a) {
			return false
		}
	}
	return true
}
