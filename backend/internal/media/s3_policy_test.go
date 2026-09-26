package media

import (
	"net/netip"
	"testing"
)

// The API E2E substitutes the external S3 transport and cannot exercise DNS
// address filtering. Keep these cases for rebinding, IPv4-mapped IPv6,
// metadata/loopback targets, and suffix lookalikes at that boundary.
func TestInternalS3NetworkPolicy(t *testing.T) {
	domains := []string{".internal", ".local", ".lan", ".home.arpa", ".localdomain", ".svc", ".cluster.local", ".company.example", "storage"}
	for _, host := range []string{"rustfs.zeabur.internal", "nas.local", "s3.lan", "nas.home.arpa", "s3.localdomain", "s3.team.svc", "s3.team.svc.cluster.local", "s3.company.example", "storage", "S3.INTERNAL."} {
		if !internalHost(host, domains) {
			t.Errorf("internal host rejected: %s", host)
		}
	}
	for _, host := range []string{"s3.internal.example.com", "notinternal", "storage.evil.example", "evilstorage", "127.0.0.1", "::1", "169.254.169.254", "s3.example.com"} {
		if internalHost(host, domains) {
			t.Errorf("untrusted host accepted: %s", host)
		}
	}
	if internalHost("s3.internal", nil) {
		t.Fatal("empty allowlist permits internal host")
	}
	for _, ip := range []string{"10.0.0.1", "172.16.0.1", "192.168.1.1", "fd00::1", "::ffff:10.0.0.1"} {
		addr := netip.MustParseAddr(ip)
		if !storageIP(addr, true) || storageIP(addr, false) {
			t.Errorf("private address policy: %s", ip)
		}
	}
	for _, ip := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "169.254.169.254", "fe80::1", "100.100.100.200", "0.0.0.0", "224.0.0.1"} {
		if storageIP(netip.MustParseAddr(ip), true) {
			t.Errorf("unsafe internal destination: %s", ip)
		}
	}
}
