package directory

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// Usage of an empty directory should be 0
func TestUsageEmpty(t *testing.T) {
	usage, err := Usage(t.TempDir())
	require.NoError(t, err)
	expectSizeAndInodeCount(t, "empty directory", usage, &DiskUsage{
		Size:       0,
		InodeCount: 1,
	})
}

// Usage of one empty file should be 0
func TestUsageEmptyFile(t *testing.T) {
	dir := t.TempDir()

	var file *os.File
	var err error
	if file, err = os.CreateTemp(dir, "file"); err != nil {
		t.Fatalf("failed to create file: %s", err)
	}

	usage, err := Usage(file.Name())
	require.NoError(t, err)
	expectSizeAndInodeCount(t, "one file", usage, &DiskUsage{
		Size:       0,
		InodeCount: 1,
	})
}

// Usage of a directory with one 5-byte file should be 5
func TestUsageNonemptyFile(t *testing.T) {
	dir := t.TempDir()

	file := filepath.Join(dir, "file")
	err := os.WriteFile(file, []byte{97, 98, 99, 100, 101}, 0o644)
	require.NoError(t, err)

	usage, err := Usage(dir)
	require.NoError(t, err)
	expectSizeAndInodeCount(t, "directory with one 5-byte file", usage, &DiskUsage{
		Size:       5,
		InodeCount: 2,
	})
}

func TestUsageSymlink(t *testing.T) {
	dir := t.TempDir()

	unexpectedDir := t.TempDir()
	err := os.WriteFile(filepath.Join(unexpectedDir, "file1"), []byte("this should not be counted"), 0o600)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(unexpectedDir, "file2"), []byte("this should not be counted either"), 0o600)
	require.NoError(t, err)

	symlink := filepath.Join(dir, "symlink")
	err = os.Symlink(unexpectedDir, symlink)
	require.NoError(t, err)

	usage, err := Usage(symlink)
	require.NoError(t, err)
	expectSizeAndInodeCount(t, "one symlink", usage, &DiskUsage{
		Size:       int64(len(unexpectedDir)),
		InodeCount: 1,
	})
}

// Usage of an empty directory should be 0
func TestUsageEmptyDirectory(t *testing.T) {
	usage, err := Usage(t.TempDir())
	require.NoError(t, err)
	expectSizeAndInodeCount(t, "one directory", usage, &DiskUsage{
		Size:       0,
		InodeCount: 1,
	})
}

// Usage of a directory with one empty directory should be 0
func TestUsageNestedDirectoryEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("failed to create nested directory: %s", err)
	}

	usage, err := Usage(dir)
	require.NoError(t, err)
	expectSizeAndInodeCount(t, "directory with one empty directory", usage, &DiskUsage{
		Size:       0,
		InodeCount: 2,
	})
}

// Test directory with 1 file and 1 empty directory
func TestUsageFileAndNestedDirectoryEmpty(t *testing.T) {
	var err error
	dir := t.TempDir()

	if err = os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("failed to create nested directory: %s", err)
	}

	file := filepath.Join(dir, "file")
	err = os.WriteFile(file, []byte{100, 111, 99, 107, 101, 114}, 0o644)
	require.NoError(t, err)

	usage, err := Usage(dir)
	require.NoError(t, err)
	expectSizeAndInodeCount(t, "directory with 6-byte file and empty directory", usage, &DiskUsage{
		Size:       6,
		InodeCount: 3,
	})
}

// Test directory with 1 file and 1 non-empty directory
func TestUsageFileAndNestedDirectoryNonempty(t *testing.T) {
	var dirNested string
	var err error
	dir := t.TempDir()

	dirNested = filepath.Join(dir, "nested")
	if err = os.Mkdir(dirNested, 0o755); err != nil {
		t.Fatalf("failed to create nested directory: %s", err)
	}

	file := filepath.Join(dir, "file")
	err = os.WriteFile(file, []byte{100, 111, 99, 107, 101, 114}, 0o644)
	require.NoError(t, err)

	nestedFile := filepath.Join(dirNested, "file")
	err = os.WriteFile(nestedFile, []byte{100, 111, 99, 107, 101, 114}, 0o644)
	require.NoError(t, err)

	usage, err := Usage(dir)
	require.NoError(t, err)
	expectSizeAndInodeCount(t, "directory with 6-byte file and nested directory with 6-byte file", usage, &DiskUsage{
		Size:       12,
		InodeCount: 4,
	})
}

// Test migration of directory to a subdir underneath itself
func TestMoveToSubdir(t *testing.T) {
	var subDir string
	var err error
	outerDir := t.TempDir()

	subDir = filepath.Join(outerDir, "testSub")
	if err = os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("failed to create subdirectory: %v", err)
	}

	// write 4 temp files in the outer dir to get moved
	filesList := []string{"a", "b", "c", "d"}
	for _, fName := range filesList {
		if file, err := os.Create(filepath.Join(outerDir, fName)); err != nil {
			t.Fatalf("couldn't create temp file %q: %v", fName, err)
		} else {
			_, err = file.WriteString(fName)
			require.NoError(t, err)
			file.Close()
		}
	}

	if err = MoveToSubdir(outerDir, filepath.Base(subDir)); err != nil {
		t.Fatalf("Error during migration of content to subdirectory: %v", err)
	}
	// validate that the files were moved to the subdirectory
	infos, err := os.ReadDir(subDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 4 {
		t.Fatalf("Should be four files in the subdir after the migration: actual length: %d", len(infos))
	}
	var results []string
	for _, info := range infos {
		results = append(results, info.Name())
	}
	sort.Strings(results)
	if !reflect.DeepEqual(filesList, results) {
		t.Fatalf("Results after migration do not equal list of files: expected: %v, got: %v", filesList, results)
	}
}

// Test a non-existing directory
func TestUsageNonExistingDirectory(t *testing.T) {
	if _, err := Usage("/thisdirectoryshouldnotexist/TestUsageNonExistingDirectory"); err == nil {
		t.Fatalf("error is expected")
	}
}

// A helper function that tests expectation of inode count and dir size against
// the found usage.
func expectSizeAndInodeCount(t *testing.T, testName string, current, expected *DiskUsage) {
	t.Helper()
	if current.Size != expected.Size {
		t.Errorf("%s has size: %d, expected %d", testName, current.Size, expected.Size)
	}
	if current.InodeCount != expected.InodeCount {
		t.Errorf("%s has inode count: %d, expected %d", testName, current.InodeCount, expected.InodeCount)
	}
}
