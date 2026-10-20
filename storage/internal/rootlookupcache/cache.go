package rootlookupcache

import (
	"fmt"
	"os"
	"path"
	"strings"
)

// Maximum number of parent directories to cache, each frame can possibly
// contain two open files.
const maxFrames = 32

type cacheFrame struct {
	dirRoot   *os.Root
	dirFile   *os.File // Can only be set if dirRoot != nil; lazily opened; nil until FileForRoot needs it
	component string
}

func (f *cacheFrame) Close() error {
	if f.dirRoot != nil {
		_ = f.dirRoot.Close()
	}
	f.dirRoot = nil

	if f.dirFile != nil {
		_ = f.dirFile.Close()
	}
	f.dirFile = nil

	return nil
}

// Cache attempts to speed up repeated path lookups in an os.Root.
//
// As of Go 1.26, operations on os.Root always open a handle for the parent directory of the target
// path before performing the actual operation. That is currently fairly costly, opening a handle
// for each path component individually (e.g. it does not use openat2 on Linux).
//
// So, try to amortize the cost: if the target path is only a basename, os.Root can just do the
// actual operation as a single system call.
// Even with openat2 this would avoid repeated openat2() lookups of the parent.
//
// This is valuable for code needing to do further I/O on files within a fs.WalkDir handler
// (sadly fs.WalkDir does not give us a parent handle, it needs one internally); or for similar
// code that needs to repeatedly make operations within a single directory.
type Cache struct {
	root     *os.Root
	rootFile *os.File // For root; or nil

	// frames caches up to maxFrames of the deepest ancestor directories of the most recently
	// accessed path, as a chain: frames[0] is a child of root, frames[1] is a child of
	// frames[0], and so on, with frames[len(frames)-1] always being the root most recently
	// returned by RootForDir/PreparePath.
	//
	// This suits fs.WalkDir-style traversals, where lookups are dominated by descending into
	// subdirectories and returning to a recent ancestor to visit its next sibling
	// (root/dir, root/dir/subdir1, root/dir, root/dir/subdir2, ...); it caches the chain of
	// ancestors, not the siblings themselves.
	//
	// The length is bounded by maxFrames so that a deep path (e.g. a/a/a/a/...) can't exhaust
	// the process' file descriptor limit; once the window is exceeded, the shallowest frame is
	// closed, and reopened later if it is needed again.
	//
	// Even for callers that implement their own manual tree walk instead of using fs.WalkDir, it is
	// usually still worth driving this cache externally rather than holding directory handles open
	// across the recursion directly: this keeps handle-lifetime management in one place (separation
	// of concerns), and bounds the number of open file descriptors to maxFrames regardless of
	// traversal depth, something a naive recursive walk, which keeps one handle open per stack
	// frame, cannot do for a deep hierarchy.
	frames []cacheFrame
}

// NewCache creates a cache for operations within root.
// The root must be valid until Close() is called.
func NewCache(root *os.Root) *Cache {
	return &Cache{
		root: root,
	}
}

func (c *Cache) truncateFrames(n int) {
	for i := n; i < len(c.frames); i++ {
		_ = c.frames[i].Close()
	}
	c.frames = c.frames[:n]
}

func (c *Cache) avoidExceedingMaxFrames(indexToOpenAfterwards int) {
	indexToClose := indexToOpenAfterwards - maxFrames
	if indexToClose < 0 {
		return
	}

	c.frames[indexToClose].Close()
}

// Close releases any possibly cached handles.
func (c *Cache) Close() error {
	if c.rootFile != nil {
		_ = c.rootFile.Close()
	}
	c.truncateFrames(0)
	return nil
}

// RootForDir returns an *os.Root for a specified directory (a fs.ValidPath) within the cache’s root.
// The returned value can be used only until the next call to RootForDir() or Close();
// the caller must not close it manually.
func (c *Cache) RootForDir(dir string) (*os.Root, error) {
	if dir == "." {
		return c.root, nil
	}

	parent := c.root
	index := 0
	for component := range strings.SplitSeq(dir, "/") {
		if index < len(c.frames) && c.frames[index].component == component && c.frames[index].dirRoot != nil {
			parent = c.frames[index].dirRoot
			index++
			continue
		}

		c.truncateFrames(index)
		c.avoidExceedingMaxFrames(index)

		dirRoot, err := parent.OpenRoot(component)
		if err != nil {
			return nil, err
		}

		c.frames = append(c.frames, cacheFrame{
			dirRoot:   dirRoot,
			component: component,
		})
		parent = dirRoot
		index++
	}

	// Ensure there aren't leftover frames if `RootForDir` opens a prefix
	// of the previously cached directory.
	c.truncateFrames(index)

	return parent, nil
}

// PreparePath returns a parent-directory *os.Root and a basename for efficiently accessing fsPath (per fs.ValidPath),
// while restricting it to the cache’s root.
//
// The returned root can be used only until the next call to RootForDir() or Close();
// the caller must not close it manually.
func (c *Cache) PreparePath(fsPath string) (*os.Root, string, error) {
	parentPath := path.Dir(fsPath)
	parentRoot, err := c.RootForDir(parentPath)
	if err != nil {
		return nil, "", err
	}
	// A path per fs.ValidPath should not contain a ".."; reject it so that we can ensure no escape from parentPath and therefore from c.root.
	basename := path.Base(fsPath)
	if basename == ".." {
		return nil, "", fmt.Errorf("internal error: trailing .. in a path that should conform to fs.ValidPath: %q", fsPath)
	}
	return parentRoot, basename, nil
}

// FileForRoot returns, or newly opens, an *os.File descriptor for reading root (which must have been the last value obtained by RootForDir or PreparePath).
// The returned value can be used only until the next call to RootForDir(), FileForRoot() or Close();
// the caller must not close it manually.
func (c *Cache) FileForRoot(root *os.Root) (*os.File, error) {
	// Ideally, we should be able to use the file descriptor already existing in *os.Root, without the extra Open(".")/Close().

	// We should not really _need_ the special case for c.root, we probably only need one cached *os.File
	// for accessing all files in a directory, and c.root is not really special WRT expected use patterns.
	//
	// But we have the RootForDir(".") special case because it is cheap to implement there, and without a
	// corresponding File cache for "." we’d need a more complex invalidation logic in RootForDir.
	if root == c.root {
		if c.rootFile == nil {
			fd, err := c.root.Open(".")
			if err != nil {
				return nil, err
			}
			c.rootFile = fd
		}
		return c.rootFile, nil
	}

	if len(c.frames) > 0 {
		deepestFrame := &c.frames[len(c.frames)-1]
		if deepestFrame.dirRoot == root {
			if deepestFrame.dirFile == nil {
				fd, err := deepestFrame.dirRoot.Open(".")
				if err != nil {
					return nil, err
				}
				deepestFrame.dirFile = fd
			}
			return deepestFrame.dirFile, nil
		}
	}

	// Otherwise we would need to open a new file descriptor, and then we would need to close it at some point,
	// i.e. we would need to be tracking it in the cache.
	// That could work under the assumption that the caller is following the cache rules, but under that assumption,
	// we should never get here.
	return nil, fmt.Errorf("internal error: FileForRoot called with an unexpected root")
}
