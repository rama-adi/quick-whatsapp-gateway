package media

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolve and validate at connection time so DNS changes cannot turn a user
// endpoint into an unapproved private or metadata request. Redirects are disabled.
func storageDial(ctx context.Context, network, address string, allowPrivate bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		ip = ip.Unmap()
		if !storageIP(ip, allowPrivate) {
			return nil, fmt.Errorf("S3 endpoint resolved to a disallowed network address")
		}
	}
	var dialer net.Dialer
	for _, ip := range ips {
		conn, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if e == nil {
			return conn, nil
		}
		err = e
	}
	if err == nil {
		err = fmt.Errorf("S3 endpoint has no addresses")
	}
	return nil, err
}
func publicIP(ip netip.Addr) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && !netip.MustParsePrefix("198.18.0.0/15").Contains(ip)
}

// A leading dot matches a DNS suffix at a label boundary; other entries match
// one hostname. Normalize case and a DNS absolute-name trailing dot.
func internalHost(host string, domains []string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	for _, domain := range domains {
		domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
		if domain == "" || domain == "." {
			continue
		}
		if strings.HasPrefix(domain, ".") {
			if strings.HasSuffix(host, domain) {
				return true
			}
		} else if host == domain {
			return true
		}
	}
	return false
}

func storageIP(ip netip.Addr, allowPrivate bool) bool {
	ip = ip.Unmap()
	return publicIP(ip) || (allowPrivate && ip.IsPrivate())
}

func storageHTTPClient(allowPrivate bool) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return storageDial(ctx, network, address, allowPrivate)
	}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Share connection pools across all transfers using the same address policy.
var publicStorageClient = storageHTTPClient(false)
var internalStorageClient = storageHTTPClient(true)

type endpointTransport struct{ domains []string }

func (t endpointTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	trusted := internalHost(req.URL.Hostname(), t.domains)
	if req.URL.Scheme != "https" && !(req.URL.Scheme == "http" && trusted) {
		return nil, fmt.Errorf("S3 endpoint must use HTTPS, or HTTP on a trusted internal domain")
	}
	if trusted {
		return internalStorageClient.Transport.RoundTrip(req)
	}
	return publicStorageClient.Transport.RoundTrip(req)
}

func client(b Bucket, c Credentials, domains []string) *s3.Client {
	httpClient := &http.Client{Transport: endpointTransport{domains: domains},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return s3.New(s3.Options{Region: b.Region, BaseEndpoint: aws.String(b.Endpoint), UsePathStyle: b.PathStyle,
		Credentials: credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""), HTTPClient: httpClient})
}
