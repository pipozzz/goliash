// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
)

// Signer is who signed an image, as the certificate of a keyless (Sigstore) signature names it: the identity
// (a CI workflow, a service account or a person's email) and the OIDC issuer that vouched for it. Goliash reads
// it from the certificate; it does not verify the certificate's chain or the signature.
type Signer struct {
	Identity string `json:"identity"`
	Issuer   string `json:"issuer,omitempty"`
}

const (
	annotationCosignCert = "dev.sigstore.cosign/certificate"
	maxSigners           = 8
)

// Fulcio's certificate extensions for the OIDC issuer: the original raw string and the DER-encoded one.
var (
	oidIssuerV1 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}
	oidIssuerV2 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
)

// certSigner reads the signer from a certificate, PEM or DER.
func certSigner(raw []byte) (Signer, bool) {
	if b, _ := pem.Decode(raw); b != nil {
		raw = b.Bytes
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		return Signer{}, false
	}
	var s Signer
	switch {
	case len(cert.URIs) > 0:
		s.Identity = cert.URIs[0].String()
	case len(cert.EmailAddresses) > 0:
		s.Identity = cert.EmailAddresses[0]
	case cert.Subject.CommonName != "":
		s.Identity = cert.Subject.CommonName
	default:
		return Signer{}, false
	}
	for _, ext := range cert.Extensions {
		switch {
		case ext.Id.Equal(oidIssuerV2):
			var v string
			if _, err := asn1.Unmarshal(ext.Value, &v); err == nil {
				s.Issuer = v
			}
		case ext.Id.Equal(oidIssuerV1) && s.Issuer == "":
			s.Issuer = string(ext.Value)
		}
	}
	s.Identity, s.Issuer = clip(s.Identity, 300), clip(s.Issuer, 200)
	return s, true
}

// bundleSigner reads the signer from a Sigstore bundle: the leaf of its verification material.
func bundleSigner(body []byte) (Signer, bool) {
	var b struct {
		VerificationMaterial struct {
			Certificate *struct {
				RawBytes string `json:"rawBytes"`
			} `json:"certificate"`
			Chain *struct {
				Certificates []struct {
					RawBytes string `json:"rawBytes"`
				} `json:"certificates"`
			} `json:"x509CertificateChain"`
		} `json:"verificationMaterial"`
	}
	if json.Unmarshal(body, &b) != nil {
		return Signer{}, false
	}
	raw := ""
	switch vm := b.VerificationMaterial; {
	case vm.Certificate != nil:
		raw = vm.Certificate.RawBytes
	case vm.Chain != nil && len(vm.Chain.Certificates) > 0:
		raw = vm.Chain.Certificates[0].RawBytes
	}
	der, err := base64.StdEncoding.DecodeString(raw)
	if raw == "" || err != nil {
		return Signer{}, false
	}
	return certSigner(der)
}

// addSigner adds a signer once.
func (ev *Evidence) addSigner(s Signer) {
	for _, x := range ev.Signers {
		if x == s {
			return
		}
	}
	if len(ev.Signers) < maxSigners {
		ev.Signers = append(ev.Signers, s)
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// SignerMatches tells whether a signer's identity matches an expected one: equal, or within it when the expected
// identity ends in * (https://github.com/acme/* for every workflow of the acme organization).
func SignerMatches(identity, expected string) bool {
	if prefix, ok := strings.CutSuffix(expected, "*"); ok {
		return strings.HasPrefix(identity, prefix)
	}
	return identity == expected
}
