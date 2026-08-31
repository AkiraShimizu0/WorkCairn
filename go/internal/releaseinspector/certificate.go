package releaseinspector

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // fingerprint identity comparison only, not a security primitive
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors for certificate selection. Callers should compare
// with errors.Is, not string content.
var (
	ErrCertBundleMalformed = errors.New("certificate bundle is malformed")
	ErrCertZeroMatches     = errors.New("no certificate matches the expected fingerprint")
	ErrCertMultipleMatches = errors.New("more than one certificate matches the expected fingerprint")
)

// NormalizeSHA1 validates that value is exactly 40 hexadecimal
// characters and returns it normalized to lowercase. Values of the
// wrong length or containing non-hex characters are rejected -- there
// is no partial or prefix match.
func NormalizeSHA1(value string) (string, error) {
	if len(value) != 40 {
		return "", fmt.Errorf("sha1 fingerprint must be exactly 40 hex characters")
	}
	lower := strings.ToLower(value)
	if _, err := hex.DecodeString(lower); err != nil {
		return "", fmt.Errorf("sha1 fingerprint must be hexadecimal")
	}
	return lower, nil
}

// CertificateMatch is the sanitized result of selecting exactly one
// certificate from a PEM bundle by its DER-recomputed SHA-1
// fingerprint. It carries no certificate content, subject name, serial
// number, issuer, or any other field beyond the validity window --
// this package makes no Developer ID Application type, policy, OID,
// EKU, or Team ID determination.
type CertificateMatch struct {
	NotBefore time.Time
	NotAfter  time.Time
	Valid     bool // true only if the current time is within [NotBefore, NotAfter]
}

// pemCertificateHeader is the exact PEM boundary line a CERTIFICATE
// block must begin with. encoding/pem.Decode itself searches forward
// through its input for the next such boundary, silently skipping any
// leading or interstitial bytes that are not part of a PEM block --
// this package never relies on that forward search. Before every
// pem.Decode call, the remaining input (after whitespace-only
// trimming) must already start with this exact literal, or the whole
// bundle is rejected immediately.
const pemCertificateHeader = "-----BEGIN CERTIFICATE-----"

// SelectCertificateBySHA1 parses pemBundle as a sequence of PEM
// CERTIFICATE blocks, DER-decodes each, and recomputes the SHA-1
// fingerprint over the raw DER bytes of each one. Selection is by this
// recomputed fingerprint only -- never by any text field a surrounding
// tool printed alongside it. Leading garbage before the first block,
// non-PEM content between blocks, trailing garbage after the last
// block, any malformed or undecodable block, and any non-CERTIFICATE
// block type all fail the entire bundle closed -- this function never
// lets a forward PEM boundary search silently skip non-PEM bytes.
// Zero matches or more than one match (including the same certificate
// appearing twice) is also fail-closed.
func SelectCertificateBySHA1(expectedSHA1Hex string, pemBundle []byte) (CertificateMatch, error) {
	normalized, err := NormalizeSHA1(expectedSHA1Hex)
	if err != nil {
		return CertificateMatch{}, err
	}

	remaining := bytes.TrimSpace(pemBundle)
	var matches []*x509.Certificate
	for len(remaining) > 0 {
		if !bytes.HasPrefix(remaining, []byte(pemCertificateHeader)) {
			return CertificateMatch{}, fmt.Errorf("%w: expected content to begin at a PEM certificate boundary", ErrCertBundleMalformed)
		}
		var block *pem.Block
		block, remaining = pem.Decode(remaining)
		if block == nil {
			return CertificateMatch{}, fmt.Errorf("%w: unparseable PEM block", ErrCertBundleMalformed)
		}
		if block.Type != "CERTIFICATE" {
			return CertificateMatch{}, fmt.Errorf("%w: unexpected PEM block type", ErrCertBundleMalformed)
		}
		cert, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil {
			return CertificateMatch{}, fmt.Errorf("%w: certificate could not be parsed", ErrCertBundleMalformed)
		}
		fingerprint := sha1.Sum(cert.Raw) //nolint:gosec // fingerprint identity comparison only
		if hex.EncodeToString(fingerprint[:]) == normalized {
			matches = append(matches, cert)
		}
		remaining = bytes.TrimSpace(remaining)
	}

	switch len(matches) {
	case 0:
		return CertificateMatch{}, ErrCertZeroMatches
	case 1:
		cert := matches[0]
		now := time.Now()
		valid := !now.Before(cert.NotBefore) && !now.After(cert.NotAfter)
		return CertificateMatch{NotBefore: cert.NotBefore, NotAfter: cert.NotAfter, Valid: valid}, nil
	default:
		return CertificateMatch{}, ErrCertMultipleMatches
	}
}
