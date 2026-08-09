package chathub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
)

func validateRemoteDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("attachment download requires a public HTTPS URL")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && unsafeAttachmentIP(ip) {
		return errors.New("attachment URL targets a non-public address")
	}
	return nil
}

func (c *Client) resolvePublicAttachmentIPs(ctx context.Context, hostname string) ([]net.IP, error) {
	if ip := net.ParseIP(hostname); ip != nil {
		if unsafeAttachmentIP(ip) {
			return nil, errors.New("attachment URL targets a non-public address")
		}
		return []net.IP{ip}, nil
	}
	lookup := c.ResolveAttachmentIPs
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]net.IP, error) {
			addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			ips := make([]net.IP, 0, len(addresses))
			for _, address := range addresses {
				ips = append(ips, address.IP)
			}
			return ips, err
		}
	}
	ips, err := lookup(ctx, hostname)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("attachment host does not resolve")
	}
	for _, ip := range ips {
		if unsafeAttachmentIP(ip) {
			return nil, errors.New("attachment URL targets a non-public address")
		}
	}
	return ips, nil
}

// downloadRemoteAttachment follows redirects itself so each hostname is
// resolved and validated exactly once. The request dials the selected IP while
// Host and TLS certificate verification remain bound to the original name.
func (c *Client) downloadRemoteAttachment(ctx context.Context, raw string) (*http.Response, *url.URL, error) {
	current, err := url.Parse(raw)
	if err != nil {
		return nil, nil, errors.New("invalid attachment URL")
	}
	for redirects := 0; redirects <= 5; redirects++ {
		if err := validateRemoteDownloadURL(current.String()); err != nil {
			return nil, nil, err
		}
		ips, err := c.resolvePublicAttachmentIPs(ctx, current.Hostname())
		if err != nil {
			return nil, nil, err
		}
		if c.PinnedHTTPSClient == nil {
			return nil, nil, errors.New("secure attachment HTTP client is unavailable")
		}
		port := current.Port()
		if port == "" {
			port = "443"
		}
		var response *http.Response
		for _, ip := range ips {
			pinned := *current
			pinned.Host = net.JoinHostPort(ip.String(), port)
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, pinned.String(), nil)
			if requestErr != nil {
				return nil, nil, errors.New("invalid attachment URL")
			}
			request.Host = current.Host
			client := c.PinnedHTTPSClient(current.Hostname())
			if client == nil {
				return nil, nil, errors.New("secure attachment HTTP client is unavailable")
			}
			oneHop := *client
			oneHop.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			response, err = oneHop.Do(request)
			if err == nil {
				break
			}
		}
		if err != nil || response == nil {
			return nil, nil, errors.New("attachment download failed")
		}
		if response.StatusCode < 300 || response.StatusCode > 399 {
			return response, current, nil
		}
		location := response.Header.Get("Location")
		_ = response.Body.Close()
		if redirects == 5 {
			return nil, nil, errors.New("too many attachment redirects")
		}
		next, locationErr := current.Parse(location)
		if locationErr != nil || location == "" {
			return nil, nil, errors.New("attachment redirect is invalid")
		}
		current = next
	}
	return nil, nil, errors.New("too many attachment redirects")
}

func unsafeAttachmentIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range attachmentSpecialUsePrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

var attachmentSpecialUsePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}
