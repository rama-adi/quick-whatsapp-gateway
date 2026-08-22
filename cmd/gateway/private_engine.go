package main

import (
	"crypto/tls"
	"errors"
	"net"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	apigateway "github.com/ramaadi/quick-whatsapp-gateway/internal/api/gateway"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/gateway/enginegrpc"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki/gatewayidentity"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func privateEngineTLSConfig(identity *gatewayidentity.Manager) (*tls.Config, error) {
	if identity == nil {
		return nil, errors.New("gateway identity required")
	}
	roots, _, err := pki.CanonicalCertPool(identity.BootstrapCA())
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return identity.GetClientCertificate(nil) }, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("API client certificate missing")
		}
		leaf := state.PeerCertificates[0]
		if leaf.IsCA || len(leaf.URIs) != 1 || leaf.URIs[0].String() != pki.APIIdentityURI {
			return errors.New("invalid API client identity")
		}
		return nil
	}}, nil
}

func startPrivateEngine(addr, gatewayID string, identity *gatewayidentity.Manager, engine *wa.ApplicationGatewayAdapter) (func(), error) {
	tlsConfig, err := privateEngineTLSConfig(identity)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)),
		grpc.MaxRecvMsgSize(apigateway.MaxEngineMessageBytes),
		grpc.MaxSendMsgSize(apigateway.MaxEngineMessageBytes))
	gatewayv1.RegisterGatewayEngineServiceServer(server, &enginegrpc.Server{GatewayID: gatewayID, Engine: engine})
	go func() { _ = server.Serve(listener) }()
	return func() { server.GracefulStop(); _ = listener.Close() }, nil
}
