package handlers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	tlsDir      = configDir + "/tls"
	tlsCertFile = tlsDir + "/server.crt"
	tlsKeyFile  = tlsDir + "/server.key"
)

// EnsureTLSCert returns paths to a usable cert/key pair, generating a
// self-signed certificate on first run if one does not already exist.
func EnsureTLSCert() (certPath, keyPath string, err error) {
	if fileExists(tlsCertFile) && fileExists(tlsKeyFile) {
		return tlsCertFile, tlsKeyFile, nil
	}
	if err := generateSelfSignedCert(); err != nil {
		return "", "", err
	}
	return tlsCertFile, tlsKeyFile, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func generateSelfSignedCert() error {
	if err := os.MkdirAll(tlsDir, 0750); err != nil {
		return fmt.Errorf("create tls dir: %w", err)
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("serial: %w", err)
	}

	host, _ := os.Hostname()
	if host == "" {
		host = "vpn-connector"
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   host,
			Organization: []string{"vpn-connector"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              certDNSNames(host),
		IPAddresses:           certIPAddresses(),
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	if err := writePEM(tlsCertFile, "CERTIFICATE", der, 0644); err != nil {
		return err
	}

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	if err := writePEM(tlsKeyFile, "EC PRIVATE KEY", keyBytes, 0600); err != nil {
		return err
	}

	log.Printf("Generated self-signed TLS certificate at %s (valid 10 years)", tlsCertFile)
	return nil
}

func certDNSNames(host string) []string {
	names := map[string]bool{"localhost": true}
	if host != "" {
		names[host] = true
		names[host+".local"] = true
	}
	if ts := GetTailscaleStatus(); ts.Hostname != "" {
		names[ts.Hostname] = true
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	return out
}

func certIPAddresses() []net.IP {
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		ips = append(ips, ipNet.IP)
	}
	return ips
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	return nil
}
