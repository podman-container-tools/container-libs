//go:build unix

package jsonproxy_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/manifest"
)

// This image is known to be x86_64 only right now.
const knownNotManifestListedImageX8664 = "docker://quay.io/coreos/11bot"

// knownNotExtantImage would be very surprising if it did exist.
const knownNotExtantImage = "docker://quay.io/centos/centos:opensusewindowsubuntu"

const knownListImage = "docker://registry.fedoraproject.org/fedora-minimal:38"

const expectedProxySemverMajor = "0.2"

// request is copied from proxy.go
// We intentionally copy to ensure that we catch any unexpected "API" changes
// in the JSON.
type request struct {
	// Method is the name of the function
	Method string `json:"method"`
	// Args is the arguments (parsed inside the function)
	Args []any `json:"args"`
}

// reply is copied from proxy.go.
type reply struct {
	// Success is true if and only if the call succeeded.
	Success bool `json:"success"`
	// Value is an arbitrary value (or values, as array/map) returned from the call.
	Value any `json:"value"`
	// PipeID is an index into open pipes, and should be passed to FinishPipe
	PipeID uint32 `json:"pipeid"`
	// Error should be non-empty if Success == false
	Error string `json:"error"`
}

// maxMsgSize is also copied from proxy.go.
const maxMsgSize = 32 * 1024

type proxy struct {
	c    *net.UnixConn
	proc *exec.Cmd
}

type pipefd struct {
	// id is the remote identifier "pipeid"
	id     uint
	datafd *os.File
	errfd  *os.File
}

func (p *proxy) call(method string, args []any) (rval any, fd *pipefd, err error) {
	req := request{
		Method: method,
		Args:   args,
	}
	reqbuf, err := json.Marshal(&req)
	if err != nil {
		return
	}
	n, err := p.c.Write(reqbuf)
	if err != nil {
		return
	}
	if n != len(reqbuf) {
		err = fmt.Errorf("short write during call of %d bytes", n)
		return
	}
	oob := make([]byte, syscall.CmsgSpace(1))
	replybuf := make([]byte, maxMsgSize)
	n, oobn, _, _, err := p.c.ReadMsgUnix(replybuf, oob)
	if err != nil {
		err = fmt.Errorf("reading reply: %w", err)
		return
	}
	var reply reply
	err = json.Unmarshal(replybuf[0:n], &reply)
	if err != nil {
		err = fmt.Errorf("Failed to parse reply: %w", err)
		return
	}
	if !reply.Success {
		err = fmt.Errorf("remote error: %s", reply.Error)
		return
	}

	var scms []syscall.SocketControlMessage
	scms, err = syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		err = fmt.Errorf("failed to parse control message: %w", err)
		return
	}
	if reply.PipeID > 0 {
		if len(scms) != 1 {
			err = fmt.Errorf("Expected 1 socket control message, found %d", len(scms))
			return
		}
	}
	if len(scms) > 2 {
		err = fmt.Errorf("Expected 1 or 2 socket control message, found %d", len(scms))
		return
	}
	if len(scms) != 0 {
		var fds []int
		fds, err = syscall.ParseUnixRights(&scms[0])
		if err != nil {
			err = fmt.Errorf("failed to parse unix rights: %w", err)
			return
		}
		if len(fds) < 1 || len(fds) > 2 {
			err = fmt.Errorf("expected 1 or 2 fds, found %d", len(fds))
			return
		}
		var errfd *os.File
		if len(fds) == 2 {
			errfd = os.NewFile(uintptr(fds[1]), "errfd")
		}
		fd = &pipefd{
			datafd: os.NewFile(uintptr(fds[0]), "replyfd"),
			id:     uint(reply.PipeID),
			errfd:  errfd,
		}
	}

	rval = reply.Value
	return
}

func (p *proxy) callNoFd(method string, args []any) (rval any, err error) {
	var fd *pipefd
	rval, fd, err = p.call(method, args)
	if err != nil {
		return
	}
	if fd != nil {
		err = fmt.Errorf("Unexpected fd from method %s", method)
		return
	}
	return rval, nil
}

func (p *proxy) callReadAllBytes(method string, args []any) (buf []byte, err error) {
	var fd *pipefd
	_, fd, err = p.call(method, args)
	if err != nil {
		return
	}
	if fd == nil {
		err = fmt.Errorf("Expected fd from method %s", method)
		return
	}
	fetchchan := make(chan byteFetch)
	go func() {
		manifestBytes, err := io.ReadAll(fd.datafd)
		fetchchan <- byteFetch{
			content: manifestBytes,
			err:     err,
		}
	}()
	_, err = p.callNoFd("FinishPipe", []any{fd.id})
	if err != nil {
		return
	}
	select {
	case fetchRes := <-fetchchan:
		err = fetchRes.err
		if err != nil {
			return
		}

		buf = fetchRes.content
	case <-time.After(5 * time.Minute):
		err = fmt.Errorf("timed out during proxy fetch")
	}
	return
}

type proxyError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (p *proxy) callGetRawBlob(args []any) (buf []byte, err error) {
	var fd *pipefd
	_, fd, err = p.call("GetRawBlob", args)
	if err != nil {
		return
	}
	if fd == nil {
		err = fmt.Errorf("Expected fds from method GetRawBlob")
		return
	}
	if fd.errfd == nil {
		err = fmt.Errorf("Expected errfd from method GetRawBlob")
		return
	}
	var wg sync.WaitGroup
	fetchchan := make(chan byteFetch, 1)
	errchan := make(chan proxyError, 1)
	wg.Go(func() {
		defer close(fetchchan)
		defer fd.datafd.Close()
		buf, err := io.ReadAll(fd.datafd)
		fetchchan <- byteFetch{
			content: buf,
			err:     err,
		}
	})
	wg.Go(func() {
		defer fd.errfd.Close()
		defer close(errchan)
		buf, err := io.ReadAll(fd.errfd)
		var proxyErr proxyError
		if err != nil {
			proxyErr.Code = "read-from-proxy"
			proxyErr.Message = err.Error()
			errchan <- proxyErr
			return
		}
		// No error, leave code+message unset
		if len(buf) == 0 {
			return
		}
		unmarshalErr := json.Unmarshal(buf, &proxyErr)
		// Shouldn't happen
		if unmarshalErr != nil {
			panic(unmarshalErr)
		}
		errchan <- proxyErr
	})
	wg.Wait()

	errMsg := <-errchan
	if errMsg.Code != "" {
		return nil, fmt.Errorf("(%s) %s", errMsg.Code, errMsg.Message)
	}
	fetchRes := <-fetchchan
	err = fetchRes.err
	if err != nil {
		return
	}

	buf = fetchRes.content
	return
}

type byteFetch struct {
	content []byte
	err     error
}

func newProxy(t *testing.T, extraArgs ...string) *proxy {
	t.Helper()

	proxyBinary := os.Getenv("JSON_PROXY_TEST_BINARY")
	if proxyBinary == "" {
		t.Skip("JSON_PROXY_TEST_BINARY is not set; skipping integration test")
	}

	fds, err := syscall.Socketpair(syscall.AF_LOCAL, syscall.SOCK_SEQPACKET, 0)
	require.NoError(t, err)
	myfd := os.NewFile(uintptr(fds[0]), "myfd")
	defer myfd.Close()
	theirfd := os.NewFile(uintptr(fds[1]), "theirfd")
	defer theirfd.Close()

	mysock, err := net.FileConn(myfd)
	require.NoError(t, err)
	unixConn, ok := mysock.(*net.UnixConn)
	require.True(t, ok, "expected *net.UnixConn, got %T", mysock)

	// Note ExtraFiles starts at 3
	args := append([]string{"--sockfd", "3"}, extraArgs...)
	proc := exec.Command(proxyBinary, args...) //nolint:gosec
	proc.Stderr = os.Stderr
	proc.ExtraFiles = append(proc.ExtraFiles, theirfd)

	err = proc.Start()
	require.NoError(t, err)

	p := &proxy{
		c:    unixConn,
		proc: proc,
	}
	t.Cleanup(p.close)

	v, err := p.callNoFd("Initialize", nil)
	require.NoError(t, err)
	semver, ok := v.(string)
	require.True(t, ok, "proxy Initialize: Unexpected value %T", v)
	require.True(t, strings.HasPrefix(semver, expectedProxySemverMajor), "Unexpected semver %s", semver)
	return p
}

func (p *proxy) close() {
	// Send Shutdown to make the server exit cleanly.
	_, _ = p.callNoFd("Shutdown", nil)
	p.c.Close()
	_ = p.proc.Wait()
}

// runTestMetadataAPIs exercises all the metadata fetching APIs.
func runTestMetadataAPIs(p *proxy, img string) error {
	v, err := p.callNoFd("OpenImage", []any{img})
	if err != nil {
		return err
	}

	imgidv, ok := v.(float64)
	if !ok {
		return fmt.Errorf("OpenImage return value is %T", v)
	}
	imgid := uint64(imgidv)
	if imgid == 0 {
		return fmt.Errorf("got zero from expected image")
	}

	// Also verify the optional path
	v, err = p.callNoFd("OpenImageOptional", []any{img})
	if err != nil {
		return err
	}

	imgidv, ok = v.(float64)
	if !ok {
		return fmt.Errorf("OpenImageOptional return value is %T", v)
	}
	imgid2 := uint64(imgidv)
	if imgid2 == 0 {
		return fmt.Errorf("got zero from expected image")
	}

	_, err = p.callNoFd("CloseImage", []any{imgid2})
	if err != nil {
		return err
	}

	manifestBytes, err := p.callReadAllBytes("GetManifest", []any{imgid})
	if err != nil {
		return err
	}
	_, err = manifest.OCI1FromManifest(manifestBytes)
	if err != nil {
		return err
	}

	configBytes, err := p.callReadAllBytes("GetFullConfig", []any{imgid})
	if err != nil {
		return err
	}
	var config imgspecv1.Image
	err = json.Unmarshal(configBytes, &config)
	if err != nil {
		return err
	}

	// Validate that the image config seems sane
	if config.Architecture == "" {
		return fmt.Errorf("No architecture found")
	}
	if len(config.Config.Cmd) == 0 && len(config.Config.Entrypoint) == 0 {
		return fmt.Errorf("No CMD or ENTRYPOINT set")
	}

	layerInfoBytes, err := p.callReadAllBytes("GetLayerInfoPiped", []any{imgid})
	if err != nil {
		return err
	}
	var layerInfoBytesData []any
	err = json.Unmarshal(layerInfoBytes, &layerInfoBytesData)
	if err != nil {
		return err
	}
	if len(layerInfoBytesData) == 0 {
		return fmt.Errorf("expected layer info data")
	}

	// Also test this legacy interface
	ctrconfigBytes, err := p.callReadAllBytes("GetConfig", []any{imgid})
	if err != nil {
		return err
	}
	var ctrconfig imgspecv1.ImageConfig
	err = json.Unmarshal(ctrconfigBytes, &ctrconfig)
	if err != nil {
		return err
	}

	// Validate that the config seems sane
	if len(ctrconfig.Cmd) == 0 && len(ctrconfig.Entrypoint) == 0 {
		return fmt.Errorf("No CMD or ENTRYPOINT set")
	}

	_, err = p.callNoFd("CloseImage", []any{imgid})
	if err != nil {
		return err
	}

	return nil
}

func runTestOpenImageOptionalNotFound(p *proxy, img string) error {
	v, err := p.callNoFd("OpenImageOptional", []any{img})
	if err != nil {
		return err
	}

	imgidv, ok := v.(float64)
	if !ok {
		return fmt.Errorf("OpenImageOptional return value is %T", v)
	}
	imgid := uint64(imgidv)
	if imgid != 0 {
		return fmt.Errorf("Unexpected optional image id %v", imgid)
	}
	return nil
}

func runTestGetBlob(p *proxy, img string) error {
	imgid, err := p.callNoFd("OpenImage", []any{img})
	if err != nil {
		return err
	}

	manifestBytes, err := p.callReadAllBytes("GetManifest", []any{imgid})
	if err != nil {
		return err
	}
	mfest, err := manifest.OCI1FromManifest(manifestBytes)
	if err != nil {
		return err
	}

	for _, layer := range mfest.Layers {
		blobBytes, err := p.callGetRawBlob([]any{imgid, layer.Digest})
		if err != nil {
			return err
		}
		if len(blobBytes) != int(layer.Size) {
			panic(fmt.Sprintf("Expected %d bytes, got %d", layer.Size, len(blobBytes)))
		}
	}

	// echo "not a valid layer" | sha256sum
	invalidDigest := "sha256:21a9aab5a3494674d2b4d8e7381c236a799384dd10545531014606cf652c119f"

	blobBytes, err := p.callGetRawBlob([]any{imgid, invalidDigest})
	if err == nil {
		panic("Expected error fetching invalid blob")
	}
	if blobBytes != nil {
		panic("Expected no bytes fetching invalid blob")
	}

	return nil
}

func TestProxyMetadata(t *testing.T) {
	p := newProxy(t)

	err := runTestMetadataAPIs(p, knownNotManifestListedImageX8664)
	if err != nil {
		err = fmt.Errorf("Testing image %s: %v", knownNotManifestListedImageX8664, err)
	}
	assert.NoError(t, err)

	err = runTestMetadataAPIs(p, knownListImage)
	if err != nil {
		err = fmt.Errorf("Testing image %s: %v", knownListImage, err)
	}
	assert.NoError(t, err)

	err = runTestOpenImageOptionalNotFound(p, knownNotExtantImage)
	if err != nil {
		err = fmt.Errorf("Testing optional image %s: %v", knownNotExtantImage, err)
	}
	assert.NoError(t, err)
}

func TestProxyGetBlob(t *testing.T) {
	p := newProxy(t)

	err := runTestGetBlob(p, knownListImage)
	if err != nil {
		err = fmt.Errorf("Testing GetBLob for %s: %v", knownListImage, err)
	}
	assert.NoError(t, err)
}

func TestProxyPolicyVerification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		policy    string
		image     string
		extraArgs []string
		wantErr   string // empty = expect success
	}{
		{
			name:   "cosign-signed image accepted",
			policy: "testdata/policy-cosign.json",
			image:  "dir:testdata/dir-img-cosign-valid",
		},
		{
			name:    "unsigned image rejected",
			policy:  "testdata/policy-cosign.json",
			image:   "dir:testdata/dir-img-unsigned",
			wantErr: "signature",
		},
		{ // The proxy checks signatures on *either* the manifest list or per-arch manifest.
			name:      "manifest-list with per-arch sig accepted",
			policy:    "testdata/policy-cosign.json",
			image:     "dir:testdata/dir-img-cosign-manifest-list-signed-arch",
			extraArgs: []string{"--override-arch", "amd64"},
		},
		{
			name:    "reject-all policy",
			policy:  "testdata/policy-reject.json",
			image:   "dir:testdata/dir-img-cosign-valid",
			wantErr: "reject",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--policy", tc.policy}, tc.extraArgs...)
			p := newProxy(t, args...)
			v, err := p.callNoFd("OpenImage", []any{tc.image})
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			imgID, ok := v.(float64)
			require.True(t, ok)
			require.NotZero(t, imgID)
			_, err = p.callNoFd("CloseImage", []any{imgID})
			require.NoError(t, err)
		})
	}
}
