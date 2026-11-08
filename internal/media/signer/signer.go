// Package signer classifies the Secure Boot signer of an EFI binary (ADR
// 0018): the embedded Authenticode signature is parsed and digest-checked
// (go-uefi), its signing certificate verified, and the certificate chained
// to one of the Microsoft UEFI CAs compiled into the binary. Validity
// periods are ignored, as UEFI firmware ignores them (the 2011 CAs have
// expired but are still in every db).
package signer

import (
	"crypto/x509"
	"embed"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/foxboron/go-uefi/authenticode"
)

// Signer is the API's SecureBootSigner.
type Signer string

// Signers.
const (
	WindowsPCA2011    Signer = "windows-pca-2011"
	WindowsUEFICA2023 Signer = "windows-uefi-ca-2023"
	UEFICA2011        Signer = "uefi-ca-2011"
	UEFICA2023        Signer = "uefi-ca-2023"
	Unsigned          Signer = "unsigned"
	Unknown           Signer = "unknown" // signed, but not verifiably by a known CA
)

// ErrNotPE: the data isn't a PE/COFF binary.
var ErrNotPE = errors.New("signer: not a PE/COFF binary")

//go:embed certs/*.crt
var certFiles embed.FS

// CA is a trusted CA and the signer it identifies.
type CA struct {
	Signer Signer
	Cert   *x509.Certificate
}

// Classifier classifies binaries against a set of CAs.
type Classifier struct {
	cas []CA
}

// New returns a Classifier for the given CAs (tests); Default uses
// Microsoft's.
func New(cas ...CA) *Classifier { return &Classifier{cas: cas} }

// Default returns a Classifier for the four Microsoft UEFI CAs.
func Default() *Classifier { return defaultClassifier }

var defaultClassifier = func() *Classifier {
	var cas []CA
	for _, s := range []Signer{WindowsUEFICA2023, WindowsPCA2011, UEFICA2023, UEFICA2011} {
		b, err := certFiles.ReadFile("certs/" + string(s) + ".crt")
		if err != nil {
			panic(err)
		}
		c, err := x509.ParseCertificate(b)
		if err != nil {
			panic(fmt.Sprintf("signer: embedded %s: %v", s, err))
		}
		cas = append(cas, CA{Signer: s, Cert: c})
	}
	return New(cas...)
}()

// Result is a classification.
type Result struct {
	Signer  Signer
	Subject string // the signing certificate's subject, if any
}

// Classify reads a PE/COFF binary of size bytes. A binary whose signatures
// don't verify, or chain to no known CA, is Unknown; no signature at all is
// Unsigned. The first signature that chains to a known CA wins.
func (c *Classifier) Classify(r io.ReaderAt, size int64) (res Result, err error) {
	// The parser and verifier work on untrusted bytes; a panic in them is a
	// malformed binary, not a crash.
	defer func() {
		if p := recover(); p != nil {
			res, err = Result{}, fmt.Errorf("%w: %v", ErrNotPE, p)
		}
	}()
	return c.classify(r, size)
}

func (c *Classifier) classify(r io.ReaderAt, size int64) (Result, error) {
	pe, err := parse(io.NewSectionReader(r, 0, size))
	if err != nil {
		return Result{}, err
	}
	sigs, err := pe.Signatures()
	if err != nil || len(sigs) == 0 {
		return Result{Signer: Unsigned}, nil
	}
	res := Result{Signer: Unknown}
	for _, sig := range sigs {
		auth, err := authenticode.ParseAuthenticode(sig.Certificate)
		if err != nil {
			continue
		}
		certs := auth.Pkcs.Certs
		for _, leaf := range certs {
			// Checks the PE digest and that leaf made the signature.
			if ok, err := pe.Verify(leaf); err != nil || !ok {
				continue
			}
			if res.Subject == "" {
				res.Subject = leaf.Subject.String()
			}
			if s, ok := c.chain(leaf, certs); ok {
				return Result{Signer: s, Subject: leaf.Subject.String()}, nil
			}
		}
	}
	return res, nil
}

// chain finds the CA leaf chains to, through the signature's other
// certificates. Times are ignored: the check runs at the leaf's own
// issuance time.
func (c *Classifier) chain(leaf *x509.Certificate, bag []*x509.Certificate) (Signer, bool) {
	inter := x509.NewCertPool()
	for _, b := range bag {
		if b != leaf {
			inter.AddCert(b)
		}
	}
	for _, ca := range c.cas {
		roots := x509.NewCertPool()
		roots.AddCert(ca.Cert)
		for _, at := range []time.Time{leaf.NotBefore.Add(time.Minute), ca.Cert.NotBefore.Add(time.Minute)} {
			_, err := leaf.Verify(x509.VerifyOptions{
				Roots: roots, Intermediates: inter, CurrentTime: at,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
			})
			if err == nil {
				return ca.Signer, true
			}
		}
	}
	return "", false
}

// parse checks the MZ signature and parses the PE/COFF structure.
func parse(r *io.SectionReader) (*authenticode.PECOFFBinary, error) {
	head := make([]byte, 2)
	if _, err := r.ReadAt(head, 0); err != nil || string(head) != "MZ" {
		return nil, ErrNotPE
	}
	pe, err := authenticode.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPE, err)
	}
	return pe, nil
}
