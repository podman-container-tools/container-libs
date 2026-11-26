package daemon

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"net/http"
	"path/filepath"
	"testing"

	dockerclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/internal/set"
	"go.podman.io/image/v5/types"
)

func TestDockerClientFromNilSystemContext(t *testing.T) {
	client, err := newDockerClient(nil)

	assert.Nil(t, err, "There should be no error creating the Docker client")
	assert.NotNil(t, client, "A Docker client reference should have been returned")

	assert.Equal(t, dockerclient.DefaultDockerHost, client.DaemonHost(), "The default docker host should have been used")

	assert.NoError(t, client.Close())
}

func TestDockerClientFromCertContext(t *testing.T) {
	host := "tcp://127.0.0.1:2376"
	systemCtx := &types.SystemContext{
		DockerDaemonCertPath:              filepath.Join("testdata", "certs"),
		DockerDaemonHost:                  host,
		DockerDaemonInsecureSkipTLSVerify: true,
	}

	client, err := newDockerClient(systemCtx)

	assert.Nil(t, err, "There should be no error creating the Docker client")
	assert.NotNil(t, client, "A Docker client reference should have been returned")

	assert.Equal(t, host, client.DaemonHost())

	assert.NoError(t, client.Close())
}

func TestTlsConfig(t *testing.T) {
	tests := []struct {
		ctx            *types.SystemContext
		wantInsecure   bool
		wantCAIncluded bool
		wantCerts      int
	}{
		{&types.SystemContext{
			DockerDaemonCertPath:              filepath.Join("testdata", "certs"),
			DockerDaemonInsecureSkipTLSVerify: true,
		}, true, false, 1},
		{&types.SystemContext{DockerDaemonInsecureSkipTLSVerify: true}, true, false, 0},
		{&types.SystemContext{
			DockerDaemonCertPath: filepath.Join("testdata", "certs"),
		}, false, true, 1},
		{&types.SystemContext{
			DockerDaemonCertPath: filepath.Join("testdata", "certs"),
			BaseTLSConfig: &tls.Config{
				MinVersion: tls.VersionTLS13,
			},
		}, false, true, 1},
	}
	for _, c := range tests {
		httpClient, err := tlsConfig(c.ctx)
		require.NoError(t, err)
		transport, ok := httpClient.Transport.(*http.Transport)
		require.True(t, ok)
		tlsCfg := transport.TLSClientConfig
		assert.Equal(t, c.wantInsecure, tlsCfg.InsecureSkipVerify)

		if c.wantCAIncluded {
			// SystemCertPool is implemented natively, and .Subjects() does not
			// return raw certificates, on some systems (as of Go 1.18,
			// Windows, macOS, iOS); so, .Subjects() is deprecated.
			// We still use .Subjects() in these tests, because they work
			// acceptably even in the native case, and they work fine on Linux,
			// which we care about the most.

			// On systems where SystemCertPool is not special-cased, RootCAs include SystemCertPool;
			// On systems where SystemCertPool is special cased, this compares two empty sets
			// and succeeds.
			// There isn’t a plausible alternative to calling .Subjects() here.
			loadedSubjectBytes := set.New[string]()
			for _, s := range tlsCfg.RootCAs.Subjects() { //nolint:staticcheck // SA1019: Receiving no data for system roots is acceptable.
				loadedSubjectBytes.Add(string(s))
			}
			systemCertPool, err := x509.SystemCertPool()
			require.NoError(t, err)
			for _, s := range systemCertPool.Subjects() { //nolint:staticcheck // SA1019: Receiving no data for system roots is acceptable.
				assert.True(t, loadedSubjectBytes.Contains(string(s)))
			}
			// RootCAs include our certificates.
			// We could possibly test this without .Subjects() by validating certificates
			// signed by our test CAs.
			loadedSubjectOrgs := set.New[string]()
			for _, s := range tlsCfg.RootCAs.Subjects() { //nolint:staticcheck // SA1019: We only care about non-system roots here.
				subjectRDN := pkix.RDNSequence{}
				rest, err := asn1.Unmarshal(s, &subjectRDN)
				require.NoError(t, err)
				require.Empty(t, rest)
				subject := pkix.Name{}
				subject.FillFromRDNSequence(&subjectRDN)
				for _, org := range subject.Organization {
					loadedSubjectOrgs.Add(org)
				}
			}
			assert.True(t, loadedSubjectOrgs.Contains("hardy"), "RootCAs should include the testdata CA (O=hardy)")
		}

		assert.Len(t, tlsCfg.Certificates, c.wantCerts)

		if c.ctx.BaseTLSConfig != nil {
			assert.Equal(t, c.ctx.BaseTLSConfig.MinVersion, tlsCfg.MinVersion)
		}
	}

	for _, c := range []struct {
		ctx          *types.SystemContext
		pathFragment string
	}{
		{&types.SystemContext{DockerDaemonCertPath: "/dev/null/this/does/not/exist"}, "dev/null/this/does/not/exist"},
		{&types.SystemContext{DockerDaemonCertPath: filepath.Join("testdata", "certs-no-ca")}, "ca.pem"},
		{&types.SystemContext{DockerDaemonCertPath: filepath.Join("testdata", "certs-no-cert")}, "cert.pem"},
		{&types.SystemContext{DockerDaemonCertPath: filepath.Join("testdata", "certs-no-key")}, "key.pem"},
		{&types.SystemContext{DockerDaemonCertPath: filepath.Join("testdata", "certs-unreadable-ca")}, "ca.pem"},
		{&types.SystemContext{DockerDaemonCertPath: filepath.Join("testdata", "certs-unreadable-cert")}, "cert.pem"},
		{&types.SystemContext{DockerDaemonCertPath: filepath.Join("testdata", "certs-unreadable-key")}, "key.pem"},
	} {
		_, err := tlsConfig(c.ctx)
		assert.ErrorContains(t, err, c.pathFragment)
	}
}

func TestSpecifyPlainHTTPViaHostScheme(t *testing.T) {
	host := "http://127.0.0.1:2376"
	ctx := &types.SystemContext{
		DockerDaemonHost: host,
	}

	client, err := newDockerClient(ctx)

	assert.Nil(t, err, "There should be no error creating the Docker client")
	assert.NotNil(t, client, "A Docker client reference should have been returned")

	assert.Equal(t, host, client.DaemonHost())
	assert.NoError(t, client.Close())
}
