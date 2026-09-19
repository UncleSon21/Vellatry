package site

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8":                true,
		"2606:4700::1111":        true,
		"127.0.0.1":              false,
		"10.1.2.3":               false,
		"172.16.0.1":             false,
		"192.168.1.1":            false,
		"169.254.169.254":        false, // cloud metadata
		"100.64.0.1":             false,
		"0.0.0.0":                false,
		"::1":                    false,
		"fe80::1":                false,
		"fc00::1":                false,
		"::ffff:10.0.0.1":        false, // v4-mapped private
		"::ffff:169.254.169.254": false,
		"64:ff9b::a00:1":         false,
		"224.0.0.1":              false,
	} {
		if got := PublicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("PublicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestDefaultCrawlerRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the guarded client reached a loopback server")
	}))
	defer srv.Close()

	c := &Crawler{}
	c.defaults()
	_, err := c.HTTP.Get(srv.URL + "/")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}

	res, err := (&Crawler{}).Crawl(context.Background(), srv.URL+"/", nil)
	if len(res.Pages) != 0 || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("crawl of a loopback site = %d pages, err %v; want no pages and ErrUnreachable", len(res.Pages), err)
	}
	if want := "private or reserved address"; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to say %q", err, want)
	}
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("err %q leaks the resolved address", err)
	}
}
