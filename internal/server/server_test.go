package server

import "testing"

const (
	testTLSPort = ":8443"
	testCert    = "/tmp/cert.pem"
)

type inboundModeCase struct {
	name       string
	listenAddr string
	cert       string
	key        string
	wantAddr   string
	wantProto  string
	wantTLS    bool
	wantErr    bool
}

func TestResolveInboundMode(t *testing.T) {
	tests := []inboundModeCase{
		{
			name:       "ohne TLS startet HTTP1",
			listenAddr: ":8080",
			wantAddr:   "http://:8080",
			wantProto:  protoHTTP11,
			wantTLS:    false,
		},
		{
			name:       "mit TLS startet HTTPS",
			listenAddr: testTLSPort,
			cert:       testCert,
			key:        "/tmp/key.pem",
			wantAddr:   "https://:8443",
			wantProto:  protoHTTP2TLS,
			wantTLS:    true,
		},
		{
			name:       "unvollstaendige TLS config gibt fehler",
			listenAddr: testTLSPort,
			cert:       testCert,
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertInboundMode(t, tc)
		})
	}
}

func assertInboundMode(t *testing.T, tc inboundModeCase) {
	t.Helper()

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
}
