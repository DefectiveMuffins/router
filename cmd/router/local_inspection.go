package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"

	"weave-os/router/internal/server"
)

// The inspector CA belongs to this process only. In particular, macOS Go does
// not honor SSL_CERT_FILE when using its native system certificate pool.
func localInspectionTransport(mode server.DeploymentMode, target, certificateFile string) (*http.Transport, error) {
	if mode != server.DeploymentModeSelfHosted || target == "" {
		return nil, errors.New("inspection trust requires a pinned selfhosted local session")
	}
	pem, err := os.ReadFile(certificateFile)
	if err != nil {
		return nil, err
	}
	// The launcher supplies public roots plus its ephemeral inspector CA.
	// A native SystemCertPool marker would bypass additional fallback roots
	// in provider transports whose TLSClientConfig is nil on macOS.
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("inspection CA file contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return transport, nil
}

func configureLocalInspection(mode server.DeploymentMode, target, certificateFile string) error {
	transport, err := localInspectionTransport(mode, target, certificateFile)
	if err != nil {
		return err
	}
	if err := os.Setenv("GODEBUG", os.Getenv("GODEBUG")+",x509usefallbackroots=1"); err != nil {
		return err
	}
	// Provider adapters construct their own transports with nil root pools.
	// SetFallbackRoots is process-local and is called only once, at boot.
	x509.SetFallbackRoots(transport.TLSClientConfig.RootCAs)
	http.DefaultTransport = transport
	return nil
}
