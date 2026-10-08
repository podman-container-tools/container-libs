package storage

import (
	"fmt"
	"os"

	drivers "go.podman.io/storage/drivers"
	"go.podman.io/storage/internal/splitfdstreamserver"
)

// SplitFDStreamBackend serves the layers of one store; it is what a
// SplitFDStreamStore socket answers requests from.
//
// This API is experimental and can be changed without bumping the major version number.
type SplitFDStreamBackend = splitfdstreamserver.StoreBackend

// SplitFDStreamLayerFiles delegates access to the files making up one layer,
// so that their content can be referenced in the stream instead of copied
// into it.
//
// This API is experimental and can be changed without bumping the major version number.
type SplitFDStreamLayerFiles = drivers.LayerFiles

// SplitFDStreamStore gives access to the splitfdstream capabilities of a
// store.  Use SplitFDStreamStoreFor to obtain one; the methods are not part
// of the Store interface.
//
// This API is experimental and can be changed without bumping the major version number.
type SplitFDStreamStore interface {
	// LayerFiles opens access to the files of a layer through the graph
	// driver; see drivers.SplitDirFDStreamDriver.
	LayerFiles(layerID string, flatNames map[string]string) (SplitFDStreamLayerFiles, error)

	// Socket returns a socket on which the store serves org.composefs.Oci
	// from backend.  The caller is responsible for closing it.
	Socket(backend SplitFDStreamBackend) (*os.File, error)
}

// SplitFDStreamStoreFor returns the splitfdstream capabilities of s.
//
// This API is experimental and can be changed without bumping the major version number.
func SplitFDStreamStoreFor(s Store) (SplitFDStreamStore, error) {
	store, ok := s.(*store)
	if !ok {
		return nil, fmt.Errorf("store implementation %T does not support splitdirfdstream operations: %w", s, drivers.ErrNotSupported)
	}
	return &splitFDStreamStore{store: store}, nil
}

// splitFDStreamStore implements SplitFDStreamStore for a *store.
type splitFDStreamStore struct {
	store *store
}

// splitFDStreamDriverLocked returns the graph driver as a
// SplitDirFDStreamDriver.  The caller must hold graphLock.
func (s *splitFDStreamStore) splitFDStreamDriverLocked() (drivers.SplitDirFDStreamDriver, error) {
	driver, ok := s.store.graphDriver.(drivers.SplitDirFDStreamDriver)
	if !ok {
		return nil, fmt.Errorf("driver %s does not support splitdirfdstream operations: %w", s.store.graphDriver.String(), drivers.ErrNotSupported)
	}
	return driver, nil
}

// LayerFiles implements SplitFDStreamStore.
func (s *splitFDStreamStore) LayerFiles(layerID string, flatNames map[string]string) (SplitFDStreamLayerFiles, error) {
	if err := s.store.startUsingGraphDriver(); err != nil {
		return nil, err
	}
	defer s.store.stopUsingGraphDriver()
	driver, err := s.splitFDStreamDriverLocked()
	if err != nil {
		return nil, err
	}
	return driver.LayerFiles(layerID, flatNames)
}

// Socket implements SplitFDStreamStore.
func (s *splitFDStreamStore) Socket(backend SplitFDStreamBackend) (*os.File, error) {
	if err := s.store.startUsingGraphDriver(); err != nil {
		return nil, err
	}
	_, err := s.splitFDStreamDriverLocked()
	s.store.stopUsingGraphDriver()
	if err != nil {
		return nil, err
	}

	s.store.varlinkServerLock.Lock()
	defer s.store.varlinkServerLock.Unlock()
	if s.store.varlinkServer == nil {
		server, err := splitfdstreamserver.NewVarlinkServer()
		if err != nil {
			return nil, err
		}
		s.store.varlinkServer = server
	}
	return s.store.varlinkServer.NewConnection(backend)
}
