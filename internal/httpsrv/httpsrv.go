// Package httpsrv provides an embedded HTTP server for plugins to register routes.
package httpsrv

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Luoyangan/LQBOT/internal/contract"
	"github.com/Luoyangan/LQBOT/internal/log"
)

// Server wraps http.Server with lifecycle management.
type Server struct {
	server   *http.Server
	mux      *http.ServeMux
	logger   *log.Logger
	port     int
	certFile string
	keyFile  string
}

// New creates a new HTTP server on the given port.
// The server is not started until Start() is called.
// If certFile and keyFile are both non-empty, the server will use HTTPS.
// The cert/key files are automatically looked up inside the "ssl/" directory.
func New(port int, certFile, keyFile string, logger *log.Logger) *Server {
	mux := http.NewServeMux()
	// Automatically resolve cert/key paths inside ssl/ directory
	if certFile != "" && !filepath.IsAbs(certFile) {
		certFile = filepath.Join("ssl", certFile)
	}
	if keyFile != "" && !filepath.IsAbs(keyFile) {
		keyFile = filepath.Join("ssl", keyFile)
	}
	return &Server{
		mux:      mux,
		logger:   logger,
		port:     port,
		certFile: certFile,
		keyFile:  keyFile,
	}
}

// ensureSelfSignedCert generates a self-signed certificate if the cert/key files don't exist.
func ensureSelfSignedCert(certFile, keyFile string, logger *log.Logger) error {
	// Check if both files already exist
	if _, err := os.Stat(certFile); err == nil {
		if _, err := os.Stat(keyFile); err == nil {
			return nil // both exist, nothing to do
		}
	}

	logger.Warn("SSL certificate not found, generating self-signed certificate",
		"cert", certFile,
		"key", keyFile,
	)

	// Ensure the directory for cert files exists
	if dir := filepath.Dir(certFile); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create cert directory: %w", err)
		}
	}

	// Generate ECDSA P256 private key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	// Build certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "LQBOT Self-Signed",
			Organization: []string{"LQBOT"},
		},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("0.0.0.0")},
		DNSNames:              []string{"localhost"},
	}

	// Self-sign
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	// Write cert file
	certOut, err := os.Create(certFile)
	if err != nil {
		return fmt.Errorf("create cert file: %w", err)
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return fmt.Errorf("encode cert: %w", err)
	}

	// Write key file
	keyOut, err := os.Create(keyFile)
	if err != nil {
		return fmt.Errorf("create key file: %w", err)
	}
	defer keyOut.Close()
	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		return fmt.Errorf("encode key: %w", err)
	}

	logger.Info("self-signed certificate generated", "cert", certFile, "key", keyFile, "valid_until", template.NotAfter.Format("2006-01-02"))
	return nil
}

// Start begins listening for HTTP(S) requests.
func (s *Server) Start() error {
	useTLS := s.certFile != "" && s.keyFile != ""

	if useTLS {
		// Auto-generate self-signed cert if files don't exist
		if err := ensureSelfSignedCert(s.certFile, s.keyFile, s.logger); err != nil {
			return fmt.Errorf("ensure ssl cert: %w", err)
		}
	}

	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: s.mux,
	}

	// Built-in health check
	s.mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	go func() {
		if useTLS {
			s.logger.Info("HTTPS server started", "port", s.port, "cert", s.certFile)
			if err := s.server.ListenAndServeTLS(s.certFile, s.keyFile); err != nil && err != http.ErrServerClosed {
				s.logger.Error("HTTPS server error", "error", err)
			}
		} else {
			s.logger.Info("HTTP server started", "port", s.port)
			if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				s.logger.Error("HTTP server error", "error", err)
			}
		}
	}()
	return nil
}

// Stop gracefully shuts down the HTTP server.
func (s *Server) Stop(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	// Use Close() which immediately closes the listener and all active connections.
	// For a full bot shutdown this is preferred — the process will exit shortly.
	// For restarts, the process re-executes so graceful shutdown isn't critical.
	return s.server.Close()
}

// Handle registers an HTTP handler for the given path.
func (s *Server) Handle(path string, handler http.HandlerFunc) {
	s.mux.HandleFunc(path, handler)
	s.logger.Info("HTTP route registered", "path", path)
}

// ServeMux returns the underlying http.ServeMux for advanced use.
func (s *Server) ServeMux() *http.ServeMux {
	return s.mux
}

// compile-time check
var _ contract.HTTPServer = (*Server)(nil)
