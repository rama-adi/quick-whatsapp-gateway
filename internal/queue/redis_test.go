package queue

import (
	"testing"
)

// TestParseRedisURL covers supported Redis URL forms, authentication/database extraction, and malformed
// configuration. Valid URLs must map to the expected asynq connection options, while invalid schemes or
// syntax return errors. This makes startup fail before workers run with an unintended Redis target.
func TestParseRedisURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantAddr string
		wantUser string
		wantPass string
		wantDB   int
		wantTLS  bool
		wantErr  bool
	}{
		{
			name:     "host only, default port",
			url:      "redis://localhost",
			wantAddr: "localhost:6379",
		},
		{
			name:     "host and port",
			url:      "redis://redis:6380",
			wantAddr: "redis:6380",
		},
		{
			name:     "db index in path",
			url:      "redis://127.0.0.1:6379/3",
			wantAddr: "127.0.0.1:6379",
			wantDB:   3,
		},
		{
			name:     "password only",
			url:      "redis://:s3cret@host:6379/0",
			wantAddr: "host:6379",
			wantPass: "s3cret",
			wantDB:   0,
		},
		{
			name:     "acl username and password",
			url:      "redis://alice:wonderland@host:6379/2",
			wantAddr: "host:6379",
			wantUser: "alice",
			wantPass: "wonderland",
			wantDB:   2,
		},
		{
			name:     "db as query param",
			url:      "redis://host:6379?db=5",
			wantAddr: "host:6379",
			wantDB:   5,
		},
		{
			name:     "tls scheme",
			url:      "rediss://secure:6380/1",
			wantAddr: "secure:6380",
			wantDB:   1,
			wantTLS:  true,
		},
		{name: "empty", url: "", wantErr: true},
		{name: "bad scheme", url: "http://host:6379", wantErr: true},
		{name: "missing host", url: "redis://", wantErr: true},
		{name: "non-numeric db path", url: "redis://host:6379/abc", wantErr: true},
		{name: "negative db", url: "redis://host:6379/-1", wantErr: true},
		{name: "nested path", url: "redis://host:6379/0/extra", wantErr: true},
		{name: "non-numeric db query", url: "redis://host:6379?db=xx", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opt, err := ParseRedisURL(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got opt %+v", opt)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if opt.Addr != tc.wantAddr {
				t.Errorf("Addr = %q, want %q", opt.Addr, tc.wantAddr)
			}
			if opt.Username != tc.wantUser {
				t.Errorf("Username = %q, want %q", opt.Username, tc.wantUser)
			}
			if opt.Password != tc.wantPass {
				t.Errorf("Password = %q, want %q", opt.Password, tc.wantPass)
			}
			if opt.DB != tc.wantDB {
				t.Errorf("DB = %d, want %d", opt.DB, tc.wantDB)
			}
			if (opt.TLSConfig != nil) != tc.wantTLS {
				t.Errorf("TLS present = %v, want %v", opt.TLSConfig != nil, tc.wantTLS)
			}
			if tc.wantTLS && opt.TLSConfig != nil {
				// ServerName must be the host without the port.
				if opt.TLSConfig.ServerName != "secure" {
					t.Errorf("TLS ServerName = %q, want secure", opt.TLSConfig.ServerName)
				}
			}
		})
	}
}
