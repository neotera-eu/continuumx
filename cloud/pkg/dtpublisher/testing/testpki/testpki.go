/*
Copyright 2026 Neotera.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package testpki creates short-lived certificate authorities and certificates for tests.
// Only test code imports it; it is not linked into the binary.
package testpki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// CA is a test certificate authority whose files live in a test's temporary directory.
type CA struct {
	t    *testing.T
	dir  string
	name string
	cert *x509.Certificate
	key  *ecdsa.PrivateKey

	// CertFile is the PEM file of the CA certificate.
	CertFile string
}

// Pair is a certificate and key in PEM files.
type Pair struct {
	CertFile string
	KeyFile  string
}

// NewCA creates a CA named name.
func NewCA(t *testing.T, name string) *CA {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA %s: %v", name, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA %s: %v", name, err)
	}
	ca := &CA{t: t, dir: t.TempDir(), name: name, cert: cert, key: key}
	ca.CertFile = ca.write(name+"-ca.crt", "CERTIFICATE", der)
	return ca
}

// Server issues a server certificate for localhost and 127.0.0.1.
func (ca *CA) Server(name string) Pair {
	return ca.issue(name, x509.ExtKeyUsageServerAuth, []string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)})
}

// Client issues a client certificate with common name name.
func (ca *CA) Client(name string) Pair {
	return ca.issue(name, x509.ExtKeyUsageClientAuth, nil, nil)
}

func (ca *CA) issue(name string, usage x509.ExtKeyUsage, dnsNames []string, ips []net.IP) Pair {
	t := ca.t
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: serial(t),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("issue %s: %v", name, err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("encode key of %s: %v", name, err)
	}
	prefix := ca.name + "-" + name
	return Pair{
		CertFile: ca.write(prefix+".crt", "CERTIFICATE", der),
		KeyFile:  ca.write(prefix+".key", "EC PRIVATE KEY", keyDER),
	}
}

func (ca *CA) write(file, blockType string, der []byte) string {
	path := filepath.Join(ca.dir, file)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
		ca.t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func serial(t *testing.T) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	return n
}
