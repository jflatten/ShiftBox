package imagecache

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errPublicDestination = errors.New("registry destination is not public HTTPS")

// The deny ranges supplement IsGlobalUnicast, which includes some reserved
// and shared addresses. Limit IPv6 to public global unicast assignments.
var specialUse = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

var globalIPv6 = netip.MustParsePrefix("2000::/3")

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Is6() && !globalIPv6.Contains(ip) {
		return false
	}
	for _, prefix := range specialUse {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func publicHTTPS(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Host == "" || u.Fragment != "" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return false
		}
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return publicIP(ip)
	}
	if !strings.Contains(host, ".") || len(host) > 253 {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home.arpa"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if char != '-' && !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') {
				return false
			}
		}
	}
	return true
}

type publicTransport struct {
	*http.Transport
}

func (t publicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !publicHTTPS(req.URL) {
		return nil, errPublicDestination
	}
	return t.Transport.RoundTrip(req)
}

func newPublicTransport(lookup func(context.Context, string, string) ([]netip.Addr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) http.RoundTripper {
	if lookup == nil {
		lookup = net.DefaultResolver.LookupNetIP
	}
	if dial == nil {
		dial = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	return publicTransport{&http.Transport{
		// Registry, token and blob requests share this transport. A redirect
		// cannot use an environment proxy or fall back to a different dialer.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, errPublicDestination
			}
			var ips []netip.Addr
			if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
				ips = []netip.Addr{ip}
			} else {
				dnsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				// Use the exact FQDN to avoid resolving through local search domains.
				ips, err = lookup(dnsCtx, "ip", strings.TrimSuffix(host, ".")+".")
				if err != nil {
					return nil, err
				}
			}
			if len(ips) == 0 {
				return nil, errPublicDestination
			}
			for _, ip := range ips {
				if !publicIP(ip) {
					return nil, errPublicDestination
				}
			}
			var dialErr error
			for _, ip := range ips {
				var conn net.Conn
				conn, dialErr = dial(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
			}
			return nil, dialErr
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  20 * time.Second,
		IdleConnTimeout:        time.Minute,
		MaxIdleConns:           32,
		MaxResponseHeaderBytes: 64 << 10,
		DisableCompression:     true,
	}}
}

func registryClient(base *http.Client) *http.Client {
	client := &http.Client{Transport: newPublicTransport(nil, nil)}
	if base != nil && base.Transport != nil {
		client.Transport = base.Transport
	}
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if !publicHTTPS(next.URL) {
			return errPublicDestination
		}
		if len(via) >= 10 {
			return errors.New("registry redirect limit reached")
		}
		if next.URL.Host != via[0].URL.Host {
			next.Header.Del("Authorization")
			next.Header.Del("Cookie")
			next.Header.Del("Referer")
		}
		return nil
	}
	return client
}

type remoteRegistry struct {
	origin     url.URL
	repository string
	client     *http.Client
	token      string // Scoped to one client request and its original repository.
}

func (r *remoteRegistry) get(ctx context.Context, method, kind, target, accept string) (*http.Response, error) {
	endpoint := r.origin
	endpoint.Path = "/v2/" + r.repository + "/" + kind + "/" + target
	send := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if r.token != "" {
			req.Header.Set("Authorization", "Bearer "+r.token)
		}
		return r.client.Do(req)
	}
	response, err := send()
	if err != nil || response.StatusCode != http.StatusUnauthorized {
		return response, err
	}
	// A redirected host may serve bytes, but only the registry itself may
	// request authorization for the repository.
	if response.Request == nil || response.Request.URL.Host != r.origin.Host {
		return response, nil
	}
	realm, service := bearerRealm(response.Header.Values("WWW-Authenticate"))
	if realm == "" {
		return response, nil
	}
	response.Body.Close()
	r.token, err = r.anonymousToken(ctx, realm, service)
	if err != nil {
		return nil, err
	}
	return send()
}

func bearerRealm(headers []string) (realm, service string) {
	for _, header := range headers {
		scheme, params, _ := strings.Cut(strings.TrimSpace(header), " ")
		if !strings.EqualFold(scheme, "bearer") {
			continue
		}
		// MIME parameters have the same token and quoted-string forms used
		// here. Split only at unquoted commas to preserve realm query values.
		insideQuote, escaped := false, false
		var fields strings.Builder
		for _, char := range params {
			switch {
			case escaped:
				escaped = false
			case insideQuote && char == '\\':
				escaped = true
			case char == '"':
				insideQuote = !insideQuote
			case char == ',' && !insideQuote:
				char = ';'
			}
			fields.WriteRune(char)
		}
		_, values, err := mime.ParseMediaType("bearer; " + fields.String())
		if err == nil && values["realm"] != "" {
			return values["realm"], values["service"]
		}
	}
	return "", ""
}

func (r *remoteRegistry) anonymousToken(ctx context.Context, realm, service string) (string, error) {
	u, err := url.Parse(realm)
	if err != nil || !publicHTTPS(u) {
		return "", errPublicDestination
	}
	query := u.Query()
	query.Del("account")
	query.Del("client_id")
	query.Del("offline_token")
	query.Set("service", service)
	query.Set("scope", "repository:"+r.repository+":pull")
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	response, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", upstreamError{status: response.StatusCode, retryAfter: response.Header.Get("Retry-After")}
	}
	var result struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return "", errors.New("invalid anonymous registry token response")
	}
	token := result.Token
	if token == "" {
		token = result.AccessToken
	}
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("invalid anonymous registry token")
	}
	return token, nil
}

type upstreamError struct {
	status     int
	retryAfter string
}

func (e upstreamError) Error() string {
	return "upstream registry returned HTTP " + strconv.Itoa(e.status)
}

func availabilityFailure(err error) bool {
	var status upstreamError
	if errors.As(err, &status) {
		return status.status >= 500 && status.status <= 599
	}
	var certificateErr x509.CertificateInvalidError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var tlsErr *tls.CertificateVerificationError
	if errors.Is(err, errPublicDestination) || errors.Is(err, context.Canceled) ||
		errors.As(err, &certificateErr) || errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostnameErr) || errors.As(err, &tlsErr) {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial" || errors.Is(err, context.DeadlineExceeded)
}
