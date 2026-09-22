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
)

// Resolve and validate at connection time so DNS changes cannot turn a user
// endpoint into a request to an internal service. Redirects are disabled.
func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
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
		if !publicIP(ip) {
			return nil, fmt.Errorf("S3 endpoint must resolve to public IP addresses")
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

var storageHTTPClient = func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = publicDial
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}()

func client(b Bucket, c Credentials) *s3.Client {
	return s3.New(s3.Options{Region: b.Region, BaseEndpoint: aws.String(b.Endpoint), UsePathStyle: b.PathStyle,
		Credentials: credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""), HTTPClient: storageHTTPClient})
}
