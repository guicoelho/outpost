package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	dataCertPath = "/data/ca.crt"
	dataKeyPath  = "/data/ca.key"
	sharedCAPath = "/ca/ca.crt"
)

// EnsureCA loads an existing CA pair or creates one if missing.
func EnsureCA() (*x509.Certificate, *rsa.PrivateKey, error) {
	if err := os.MkdirAll(filepath.Dir(dataCertPath), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create /data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(sharedCAPath), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create /ca dir: %w", err)
	}

	if _, err := os.Stat(dataCertPath); errors.Is(err, os.ErrNotExist) {
		if err := generate(); err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, fmt.Errorf("stat cert: %w", err)
	}

	if _, err := os.Stat(dataKeyPath); errors.Is(err, os.ErrNotExist) {
		if err := generate(); err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, fmt.Errorf("stat key: %w", err)
	}

	cert, key, err := load()
	if err != nil {
		return nil, nil, err
	}

	crtPEM, err := os.ReadFile(dataCertPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read cert for sharing: %w", err)
	}
	if err := os.WriteFile(sharedCAPath, crtPEM, 0o644); err != nil {
		return nil, nil, fmt.Errorf("write shared cert: %w", err)
	}

	return cert, key, nil
}

func generate() error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generate rsa key: %w", err)
	}

	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName: "Box Outbound Proxy CA",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	if err := os.WriteFile(dataCertPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	if err := os.WriteFile(dataKeyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}

	return nil
}

func load() (*x509.Certificate, *rsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(dataCertPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read cert: %w", err)
	}
	keyPEM, err := os.ReadFile(dataKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read key: %w", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("decode cert pem: empty block")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse cert: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("decode key pem: empty block")
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse key: %w", err)
	}

	return cert, key, nil
}
