//go:build linux

package splitfdstream

import (
	"crypto/sha256"
	"os"

	"golang.org/x/sys/unix"
)

const (
	// MaxFDsPerFrame is the maximum number of FDs that can be sent in
	// a single sendmsg() call, matching Linux SCM_MAX_FD.
	MaxFDsPerFrame = 253
)

// SeedFromID computes the SHA-256 seed from a layer identifier.
// This seed drives the sparse dirfd layout so that dummy slots
// are inserted deterministically.
func SeedFromID(layerID string) [32]byte {
	return sha256.Sum256([]byte(layerID))
}

// BuildFDLayout constructs the sparse FD array that the composefs-rs
// protocol expects.  It takes a pipe read-end (carrying splitdirfdstream
// bytes) and a set of real directory FDs, and returns:
//   - allFDs:   the full FD array to send via SCM_RIGHTS
//   - dirCount: the value to put in GetLayerReply.dir_count
//
// Layout:
//
//	fds[0]              = pipeRead
//	fds[1..=dirCount]   = sparse dirfd region (real dirs + dummies)
//	fds[dirCount+1..]   = lifetime keepalive pipe + optional extras
//
// The seed (from SeedFromID) controls how many dummy slots are inserted
// and where.  dirfdIndices maps each real dirFD's position in dirFDs to
// its index in the sparse region, so the stream writer can use the
// correct dirfd_index in FileBackedData chunks.
func BuildFDLayout(seed [32]byte, pipeRead *os.File, dirFDs []*os.File) (allFDs []*os.File, dirCount int, dirfdIndices []int, keepaliveWrite *os.File, err error) {
	nReal := len(dirFDs)
	if nReal == 0 {
		keepR, keepW, err := os.Pipe()
		if err != nil {
			return nil, 0, nil, nil, err
		}
		return []*os.File{pipeRead, keepR}, 0, nil, keepW, nil
	}

	// Derive layout parameters from seed.
	nDummies := 1 + int(seed[0])%3
	nExtra := int(seed[1]) % 3
	sparseSize := nReal + nDummies
	dirCount = sparseSize

	// Decide which slots in [0, sparseSize) hold real dirFDs.
	// Spread them deterministically based on the seed.
	realSlots := make([]int, nReal)
	used := make(map[int]bool)
	for i := range nReal {
		seedByte := seed[2+i%30]
		slot := int(seedByte) % sparseSize
		for used[slot] {
			slot = (slot + 1) % sparseSize
		}
		used[slot] = true
		realSlots[i] = slot
	}

	// Build the sparse dirfd region.
	region := make([]*os.File, sparseSize)
	dirfdIndices = make([]int, nReal)
	for i, slot := range realSlots {
		region[slot] = dirFDs[i]
		// +1 because fds[0] is the pipe
		dirfdIndices[i] = slot + 1
	}

	// Fill dummy slots with /dev/null.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	for i := range region {
		if region[i] == nil {
			region[i] = devNull
		}
	}

	// Build keepalive pipe.
	keepR, keepW, err := os.Pipe()
	if err != nil {
		devNull.Close()
		return nil, 0, nil, nil, err
	}

	allFDs = make([]*os.File, 0, 1+sparseSize+1+nExtra)
	allFDs = append(allFDs, pipeRead)
	allFDs = append(allFDs, region...)
	allFDs = append(allFDs, keepR)

	// Append extra lifetime FDs (memfds).
	for range nExtra {
		fd, err := unix.MemfdCreate("dummy", unix.MFD_CLOEXEC)
		if err != nil {
			keepW.Close()
			return nil, 0, nil, nil, err
		}
		allFDs = append(allFDs, os.NewFile(uintptr(fd), "extra-lifetime"))
	}

	return allFDs, dirCount, dirfdIndices, keepW, nil
}
