package sigstore

import (
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/secure-systems-lab/go-securesystemslib/encrypted"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/internal/signature"
	internalSigner "go.podman.io/image/v5/internal/signer"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/signature/internal"
)

func generateTestKey(t *testing.T, algorithm string) crypto.Signer {
	t.Helper()
	var key crypto.Signer
	var err error
	switch algorithm {
	case "RSA":
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	case "ECDSA":
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "Ed25519":
		_, key, err = ed25519.GenerateKey(rand.Reader)
	case "ML-DSA-44":
		key, err = mldsa.GenerateKey(mldsa.MLDSA44())
	case "ML-DSA-65":
		key, err = mldsa.GenerateKey(mldsa.MLDSA65())
	case "ML-DSA-87":
		key, err = mldsa.GenerateKey(mldsa.MLDSA87())
	default:
		t.Fatalf("unknown key algorithm %q", algorithm)
	}
	require.NoError(t, err)
	return key
}

func TestLoadPrivateKeySignAndVerify(t *testing.T) {
	testManifest := []byte(`{"schemaVersion":2}`)
	testDockerReference, err := reference.ParseNormalizedNamed("example.com/foo:notlatest")
	require.NoError(t, err)
	passphrase := []byte("some passphrase")
	const verificationFailure = "cryptographic signature verification failed"

	for _, algorithm := range []string{"RSA", "ECDSA", "Ed25519", "ML-DSA-44", "ML-DSA-65", "ML-DSA-87"} {
		t.Run(algorithm, func(t *testing.T) {
			key := generateTestKey(t, algorithm)
			privateKeyPEM, publicKeyPEM, err := marshalKeyPair(key, key.Public(), passphrase)
			require.NoError(t, err)
			privateKeyFile := filepath.Join(t.TempDir(), "private.key")
			err = os.WriteFile(privateKeyFile, privateKeyPEM, 0o600)
			require.NoError(t, err)

			_, err = NewSigner(WithPrivateKeyFile(privateKeyFile, []byte("wrong passphrase")))
			assert.Error(t, err)

			signer, err := NewSigner(WithPrivateKeyFile(privateKeyFile, passphrase))
			require.NoError(t, err)
			defer signer.Close()
			sig0, err := internalSigner.SignImageManifest(context.Background(), signer, testManifest, testDockerReference)
			require.NoError(t, err)
			sig, ok := sig0.(signature.Sigstore)
			require.True(t, ok)
			payload := sig.UntrustedPayload()
			base64Signature := sig.UntrustedAnnotations()[signature.SigstoreSignatureAnnotationKey]

			publicKey, err := cryptoutils.UnmarshalPEMToPublicKey(publicKeyPEM)
			require.NoError(t, err)
			assert.NoError(t, cryptoutils.EqualKeys(key.Public(), publicKey))

			rules := internal.SigstorePayloadAcceptanceRules{
				ValidateSignedDockerReference: func(ref string) error {
					assert.Equal(t, "example.com/foo:notlatest", ref)
					return nil
				},
				ValidateSignedDockerManifestDigest: func(digest digest.Digest) error {
					matches, err := manifest.MatchesDigest(testManifest, digest)
					require.NoError(t, err)
					assert.True(t, matches)
					return nil
				},
			}

			// The signature is accepted with the matching public key.
			_, err = internal.VerifySigstorePayload([]crypto.PublicKey{publicKey}, payload, base64Signature, rules)
			assert.NoError(t, err)

			// A different key of the same algorithm is rejected, but does not prevent
			// acceptance when the signing key is also trusted.
			otherKey := generateTestKey(t, algorithm)
			_, err = internal.VerifySigstorePayload([]crypto.PublicKey{otherKey.Public()}, payload, base64Signature, rules)
			assert.ErrorContains(t, err, verificationFailure)
			_, err = internal.VerifySigstorePayload([]crypto.PublicKey{otherKey.Public(), publicKey}, payload, base64Signature, rules)
			assert.NoError(t, err)

			// A key using a different algorithm or ML-DSA parameter set is rejected.
			for _, otherAlgorithm := range []string{"ECDSA", "ML-DSA-44", "ML-DSA-87"} {
				if otherAlgorithm == algorithm {
					continue
				}
				otherKey := generateTestKey(t, otherAlgorithm)
				_, err = internal.VerifySigstorePayload([]crypto.PublicKey{otherKey.Public()}, payload, base64Signature, rules)
				assert.ErrorContains(t, err, verificationFailure, otherAlgorithm)
			}

			// A tampered payload is rejected.
			tamperedPayload := append([]byte{}, payload...)
			tamperedPayload[len(tamperedPayload)-2] ^= 0x01
			_, err = internal.VerifySigstorePayload([]crypto.PublicKey{publicKey}, tamperedPayload, base64Signature, rules)
			assert.ErrorContains(t, err, verificationFailure)

			// A tampered signature is rejected.
			tamperedSignature := []byte(base64Signature)
			if tamperedSignature[0] == 'A' {
				tamperedSignature[0] = 'B'
			} else {
				tamperedSignature[0] = 'A'
			}
			_, err = internal.VerifySigstorePayload([]crypto.PublicKey{publicKey}, payload, string(tamperedSignature), rules)
			assert.ErrorContains(t, err, verificationFailure)
		})
	}
}

func TestLoadPrivateKeyUnsupported(t *testing.T) {
	passphrase := []byte("some passphrase")
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	x509Encoded, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	encBytes, err := encrypted.Encrypt(x509Encoded, passphrase)
	require.NoError(t, err)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Bytes: encBytes, Type: sigstorePrivateKeyPemType})

	_, err = loadPrivateKey(privateKeyPEM, passphrase)
	assert.ErrorContains(t, err, "unsupported key type")
}
