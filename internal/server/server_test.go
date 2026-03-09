package server

import "testing"

func TestResolveInboundMode(t *testing.T) {
	tests := []struct {
		name       string
		listenAddr string
		cert       string
		key        string
		wantAddr   string
		wantProto  string
		wantTLS    bool
		wantErr    bool
	}{
		{
			name:       "ohne TLS startet HTTP1",
			listenAddr: ":8080",
			wantAddr:   "http://:8080",
			wantProto:  "HTTP/1.1",
			wantTLS:    false,
		},
		{
			name:       "mit TLS startet HTTPS",
			listenAddr: ":8443",
			cert:       "/tmp/cert.pem",
			key:        "/tmp/key.pem",
			wantAddr:   "https://:8443",
			wantProto:  "HTTP/2 via ALPN",
			wantTLS:    true,
		},
		{
			name:       "unvollstaendige TLS config gibt fehler",
			listenAddr: ":8443",
			cert:       "/tmp/cert.pem",
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, proto, useTLS, err := resolveInboundMode(tc.listenAddr, tc.cert, tc.key)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("erwarte Fehler, bekam keinen")
				}
				return
			}
			if err != nil {
				t.Fatalf("unerwarteter Fehler: %v", err)
			}
			if addr != tc.wantAddr {
				t.Fatalf("addr: erwartet %q, bekam %q", tc.wantAddr, addr)
			}
			if proto != tc.wantProto {
				t.Fatalf("proto: erwartet %q, bekam %q", tc.wantProto, proto)
			}
			if useTLS != tc.wantTLS {
				t.Fatalf("useTLS: erwartet %v, bekam %v", tc.wantTLS, useTLS)
			}
		})
	}
}
