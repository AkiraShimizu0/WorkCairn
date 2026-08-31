package releaseinspector

import (
	"crypto/sha1" //nolint:gosec // fingerprint identity comparison only
	"encoding/hex"
	"encoding/pem"
	"errors"
	"testing"
)

// Fixed, synthetic, self-signed test-only certificates. None of these
// are real -- they are never issued by any certificate authority, are
// not associated with any real person or organization, and carry no
// meaning outside this test file. Their content is fixed (not
// regenerated at test time) so tests are fully reproducible.
const (
	syntheticCertValidPEM = `-----BEGIN CERTIFICATE-----
MIIBVTCB+6ADAgECAgEBMAoGCCqGSM49BAMCMDMxMTAvBgNVBAMTKHdvcmtjYWly
bi1yZWxlYXNlLWluc3BlY3Rvci10ZXN0LWZpeHR1cmUwIBcNMjAwMTAxMDAwMDAw
WhgPMjA5OTAxMDEwMDAwMDBaMDMxMTAvBgNVBAMTKHdvcmtjYWlybi1yZWxlYXNl
LWluc3BlY3Rvci10ZXN0LWZpeHR1cmUwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNC
AAQi2Qmo4tMhRQA9VCTIQU3ERJtDkCMORNMs4auuxqJu6Di2LM2gea/krHLoqmRB
Kfebz6Ey6VWuHJtm/fqSspxyMAoGCCqGSM49BAMCA0kAMEYCIQDQpKAoKQ0fzNMa
4s1wJlTDDabkE/HUdvlTuypNM8XxoQIhAMyystwFus3q39wOhLi3ausdxl1LveNR
0OKgMtNStKla
-----END CERTIFICATE-----
`

	syntheticCertExpiredPEM = `-----BEGIN CERTIFICATE-----
MIIBUjCB+aADAgECAgECMAoGCCqGSM49BAMCMDMxMTAvBgNVBAMTKHdvcmtjYWly
bi1yZWxlYXNlLWluc3BlY3Rvci10ZXN0LWZpeHR1cmUwHhcNMDAwMTAxMDAwMDAw
WhcNMDEwMTAxMDAwMDAwWjAzMTEwLwYDVQQDEyh3b3JrY2Fpcm4tcmVsZWFzZS1p
bnNwZWN0b3ItdGVzdC1maXh0dXJlMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE
I7RSEngLaS0YFSoKJkzQczY7LvInmD5ozUYTksyRrQ6AaIN00fYqXdR6R6VKCa9m
WJsYQM09rpcVjIUvQSBX4TAKBggqhkjOPQQDAgNIADBFAiEA9eQqfIiWnLxYFmxu
WjXek4zqAEhWq+ix1joJDoof/B4CIFfx2p0hdSpqnhIUByeqdKWiuzmBUahUjEFF
k1y2z7PF
-----END CERTIFICATE-----
`

	syntheticCertNotYetValidPEM = `-----BEGIN CERTIFICATE-----
MIIBVjCB/aADAgECAgEDMAoGCCqGSM49BAMCMDMxMTAvBgNVBAMTKHdvcmtjYWly
bi1yZWxlYXNlLWluc3BlY3Rvci10ZXN0LWZpeHR1cmUwIhgPMjA5OTAxMDEwMDAw
MDBaGA8yMTk5MDEwMTAwMDAwMFowMzExMC8GA1UEAxMod29ya2NhaXJuLXJlbGVh
c2UtaW5zcGVjdG9yLXRlc3QtZml4dHVyZTBZMBMGByqGSM49AgEGCCqGSM49AwEH
A0IABPet2PyAydlDOowJqcKcDst27g0tSBIYnnYe4bT0ypYV8I0lrPPjQzqz58Iw
LeeIxlqaQ6z8hFnHAQC9frxYG2MwCgYIKoZIzj0EAwIDSAAwRQIgY5Ixp+4p/Oq5
YoRzPljU+7g4yVmqbPuZVffZFJfXNZACIQD9yts9NaoSqesnFUIezhn429gjEVWC
gzWZ9oo994SfdA==
-----END CERTIFICATE-----
`
)

// syntheticCertSHA1 recomputes the DER SHA-1 fingerprint of a fixed
// synthetic PEM certificate directly (independent of the function
// under test), so tests never assume the code under test is correct
// when constructing their own expected values.
func syntheticCertSHA1(t *testing.T, certPEM string) string {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatalf("fixed test fixture is not valid PEM")
	}
	sum := sha1.Sum(block.Bytes) //nolint:gosec // fingerprint identity comparison only
	return hex.EncodeToString(sum[:])
}

func TestNormalizeSHA1(t *testing.T) {
	valid := "AABBCCDDEEFF00112233445566778899AABBCCDD"
	normalized, err := NormalizeSHA1(valid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if normalized != "aabbccddeeff00112233445566778899aabbccdd" {
		t.Fatalf("normalized = %q", normalized)
	}

	cases := []string{
		"",
		"abc",
		"aabbccddeeff00112233445566778899aabbccd",   // 39 chars
		"aabbccddeeff00112233445566778899aabbccdda", // 41 chars
		"zzbbccddeeff00112233445566778899aabbccdd",  // non-hex
	}
	for _, invalid := range cases {
		if _, err := NormalizeSHA1(invalid); err == nil {
			t.Errorf("NormalizeSHA1(%q) = nil error, want error", invalid)
		}
	}
}

func TestSelectCertificateBySHA1ExactMatchCurrentlyValid(t *testing.T) {
	sha1Hex := syntheticCertSHA1(t, syntheticCertValidPEM)
	match, err := SelectCertificateBySHA1(sha1Hex, []byte(syntheticCertValidPEM))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !match.Valid {
		t.Fatalf("expected Valid=true for a currently-in-window certificate")
	}
}

func TestSelectCertificateBySHA1ExpiredCertificate(t *testing.T) {
	sha1Hex := syntheticCertSHA1(t, syntheticCertExpiredPEM)
	match, err := SelectCertificateBySHA1(sha1Hex, []byte(syntheticCertExpiredPEM))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if match.Valid {
		t.Fatalf("expected Valid=false for an expired certificate")
	}
}

func TestSelectCertificateBySHA1NotYetValidCertificate(t *testing.T) {
	sha1Hex := syntheticCertSHA1(t, syntheticCertNotYetValidPEM)
	match, err := SelectCertificateBySHA1(sha1Hex, []byte(syntheticCertNotYetValidPEM))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if match.Valid {
		t.Fatalf("expected Valid=false for a not-yet-valid certificate")
	}
}

func TestSelectCertificateBySHA1ZeroMatches(t *testing.T) {
	unrelatedSHA1 := "0000000000000000000000000000000000000000"
	_, err := SelectCertificateBySHA1(unrelatedSHA1, []byte(syntheticCertValidPEM))
	if !errors.Is(err, ErrCertZeroMatches) {
		t.Fatalf("err = %v, want ErrCertZeroMatches", err)
	}
}

func TestSelectCertificateBySHA1MultipleMatches(t *testing.T) {
	sha1Hex := syntheticCertSHA1(t, syntheticCertValidPEM)
	bundle := syntheticCertValidPEM + syntheticCertValidPEM // same certificate, twice
	_, err := SelectCertificateBySHA1(sha1Hex, []byte(bundle))
	if !errors.Is(err, ErrCertMultipleMatches) {
		t.Fatalf("err = %v, want ErrCertMultipleMatches", err)
	}
}

func TestSelectCertificateBySHA1MalformedBundleNotPEM(t *testing.T) {
	_, err := SelectCertificateBySHA1("aabbccddeeff00112233445566778899aabbccdd", []byte("not a pem bundle at all"))
	if !errors.Is(err, ErrCertBundleMalformed) {
		t.Fatalf("err = %v, want ErrCertBundleMalformed", err)
	}
}

func TestSelectCertificateBySHA1MalformedBundleTrailingGarbage(t *testing.T) {
	sha1Hex := syntheticCertSHA1(t, syntheticCertValidPEM)
	bundle := syntheticCertValidPEM + "\ntrailing garbage that is not PEM\n"
	_, err := SelectCertificateBySHA1(sha1Hex, []byte(bundle))
	if !errors.Is(err, ErrCertBundleMalformed) {
		t.Fatalf("err = %v, want ErrCertBundleMalformed", err)
	}
}

func TestSelectCertificateBySHA1MalformedBundleTruncatedBlock(t *testing.T) {
	sha1Hex := syntheticCertSHA1(t, syntheticCertValidPEM)
	truncated := syntheticCertValidPEM[:len(syntheticCertValidPEM)-40] // cut off before the footer
	_, err := SelectCertificateBySHA1(sha1Hex, []byte(truncated))
	if !errors.Is(err, ErrCertBundleMalformed) {
		t.Fatalf("err = %v, want ErrCertBundleMalformed", err)
	}
}

func TestSelectCertificateBySHA1MalformedBundleWrongBlockType(t *testing.T) {
	block := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("not actually key material")})
	_, err := SelectCertificateBySHA1("aabbccddeeff00112233445566778899aabbccdd", block)
	if !errors.Is(err, ErrCertBundleMalformed) {
		t.Fatalf("err = %v, want ErrCertBundleMalformed", err)
	}
}

func TestSelectCertificateBySHA1EmptyBundleIsZeroMatches(t *testing.T) {
	_, err := SelectCertificateBySHA1("aabbccddeeff00112233445566778899aabbccdd", []byte(""))
	if !errors.Is(err, ErrCertZeroMatches) {
		t.Fatalf("err = %v, want ErrCertZeroMatches", err)
	}
}

func TestSelectCertificateBySHA1InvalidExpectedFingerprint(t *testing.T) {
	_, err := SelectCertificateBySHA1("not-hex", []byte(syntheticCertValidPEM))
	if err == nil {
		t.Fatalf("expected an error for an invalid expected fingerprint")
	}
}

func TestSelectCertificateBySHA1RejectsLeadingGarbage(t *testing.T) {
	sha1Hex := syntheticCertSHA1(t, syntheticCertValidPEM)
	bundle := "leading garbage that is not PEM\n" + syntheticCertValidPEM
	_, err := SelectCertificateBySHA1(sha1Hex, []byte(bundle))
	if !errors.Is(err, ErrCertBundleMalformed) {
		t.Fatalf("err = %v, want ErrCertBundleMalformed for leading garbage", err)
	}
}

func TestSelectCertificateBySHA1RejectsInterstitialGarbage(t *testing.T) {
	sha1Hex := syntheticCertSHA1(t, syntheticCertValidPEM)
	bundle := syntheticCertValidPEM + "interstitial garbage between blocks\n" + syntheticCertExpiredPEM
	_, err := SelectCertificateBySHA1(sha1Hex, []byte(bundle))
	if !errors.Is(err, ErrCertBundleMalformed) {
		t.Fatalf("err = %v, want ErrCertBundleMalformed for interstitial garbage between blocks", err)
	}
}

func TestSelectCertificateBySHA1AllowsWhitespaceBetweenBlocks(t *testing.T) {
	sha1Hex := syntheticCertSHA1(t, syntheticCertValidPEM)
	bundle := syntheticCertValidPEM + "\n\n  \t\n\n" + syntheticCertExpiredPEM
	match, err := SelectCertificateBySHA1(sha1Hex, []byte(bundle))
	if err != nil {
		t.Fatalf("unexpected error for whitespace-only separation between valid blocks: %v", err)
	}
	if !match.Valid {
		t.Fatalf("expected the whitespace-separated valid certificate to still be selected and valid")
	}
}

func TestSelectCertificateBySHA1AllowsValidMultiCertificateBundle(t *testing.T) {
	expiredSHA1 := syntheticCertSHA1(t, syntheticCertExpiredPEM)
	bundle := syntheticCertValidPEM + syntheticCertExpiredPEM + syntheticCertNotYetValidPEM
	match, err := SelectCertificateBySHA1(expiredSHA1, []byte(bundle))
	if err != nil {
		t.Fatalf("unexpected error selecting the middle certificate of a valid multi-certificate bundle: %v", err)
	}
	if match.Valid {
		t.Fatalf("expected the selected (expired) certificate to be Valid=false")
	}
}
