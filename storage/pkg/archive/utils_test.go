package archive

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var testUntarFns = map[string]func(string, io.Reader) error{
	"untar": func(dest string, r io.Reader) error {
		return Untar(r, dest, nil)
	},
	"applylayer": func(dest string, r io.Reader) error {
		_, err := ApplyLayer(dest, r)
		return err
	},
}

// testBreakout is a helper function that, within the provided `tmpdir` directory,
// creates a `victim` folder and a sibling `destx` folder, each with a generated
// `hello` file. `untar` extracts to a directory named `dest`, the tar file
// created from `headers`.
//
// Here are the tested scenarios:
// - removed `victim` or `destx` folder				(write)
// - removed files from `victim` or `destx` folder		(write)
// - new files in `victim` or `destx` folder			(write)
// - modified files in `victim` or `destx` folder		(write)
// - file in `dest` with same content as `victim/hello` or `destx/hello` (read)
//
// When using testBreakout make sure you cover one of the scenarios listed above.
func testBreakout(t *testing.T, untarFn string, headers []*tar.Header) error {
	tmpdir := t.TempDir()

	dest := filepath.Join(tmpdir, "dest")
	if err := os.Mkdir(dest, 0o755); err != nil {
		return err
	}

	destx := filepath.Join(tmpdir, "destx")
	if err := os.Mkdir(destx, 0o755); err != nil {
		return err
	}
	destxHello := filepath.Join(destx, "hello")

	victim := filepath.Join(tmpdir, "victim")
	if err := os.Mkdir(victim, 0o755); err != nil {
		return err
	}
	victimHello := filepath.Join(victim, "hello")

	helloData, err := time.Now().MarshalText()
	if err != nil {
		return err
	}

	if err := os.WriteFile(victimHello, helloData, 0o644); err != nil {
		return err
	}
	helloStat, err := os.Stat(victimHello)
	if err != nil {
		return err
	}

	if err := os.WriteFile(destxHello, helloData, 0o644); err != nil {
		return err
	}
	destxHelloStat, err := os.Stat(destxHello)
	if err != nil {
		return err
	}

	reader, writer := io.Pipe()
	go func() {
		tw := tar.NewWriter(writer)
		for _, hdr := range headers {
			err := tw.WriteHeader(hdr)
			require.NoError(t, err)
		}
		tw.Close()
	}()

	untar := testUntarFns[untarFn]
	if untar == nil {
		return fmt.Errorf("could not find untar function %q in testUntarFns", untarFn)
	}
	if err := untar(dest, reader); err != nil {
		if _, ok := err.(breakoutError); !ok {
			// If untar returns an error unrelated to an archive breakout,
			// then consider this an unexpected error and abort.
			return err
		}
		// Here, untar detected the breakout.
		// Let's move on verifying that indeed there was no breakout.
		t.Logf("breakoutError: %v\n", err)
	}

	if err := checkBreakoutSensitiveDir(victim, "hello", helloData, helloStat); err != nil {
		return err
	}
	if err := checkBreakoutSensitiveDir(destx, "hello", helloData, destxHelloStat); err != nil {
		return err
	}

	// Check that nothing in dest/ has the same content as victim/hello or destx/hello.
	// Since those files were generated with time.Now(), it is safe to assume that any
	// file in dest/ whose content matches exactly, managed somehow to access them.
	return checkBreakoutNoLeakInDest(dest, []breakoutLeak{
		{path: victimHello, data: helloData},
		{path: destxHello, data: helloData},
	})
}

type breakoutLeak struct {
	path string
	data []byte
}

func checkBreakoutSensitiveDir(dir, fileName string, helloData []byte, helloStat os.FileInfo) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("archive breakout: error reading %q: %w", dir, err)
	}
	defer f.Close()

	// We are only interested in getting 2 files from the directory, because if all is well
	// we expect only one result, the sensitive file. If there is a second result, it cannot
	// hold the same name and we assume that a new file got created in the directory.
	names, err := f.Readdirnames(2)
	if err != nil {
		return fmt.Errorf("archive breakout: error reading directory content of %q: %w", dir, err)
	}
	for _, name := range names {
		if name != fileName {
			return fmt.Errorf("archive breakout: new file %q in %q", name, dir)
		}
	}

	hello := filepath.Join(dir, fileName)
	f, err = os.Open(hello)
	if err != nil {
		return fmt.Errorf("archive breakout: could not open %q: %w", hello, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if helloStat.IsDir() != fi.IsDir() ||
		// TODO: cannot check for fi.ModTime() change
		helloStat.Mode() != fi.Mode() ||
		helloStat.Size() != fi.Size() ||
		!bytes.Equal(helloData, b) {
		// codepath taken if hello has been modified
		return fmt.Errorf("archive breakout: file %q has been modified. Contents: expected=%q, got=%q. FileInfo: expected=%#v, got=%#v", hello, helloData, b, helloStat, fi)
	}
	return nil
}

func checkBreakoutNoLeakInDest(dest string, leaks []breakoutLeak) error {
	return filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			if err != nil {
				// skip directory if error
				return filepath.SkipDir
			}
			// enter directory
			return nil
		}
		if err != nil {
			// skip file if error
			return nil //nolint: nilerr
		}
		b, err := os.ReadFile(path)
		if err != nil {
			// Houston, we have a problem. Aborting (space)walk.
			return err
		}
		for _, leak := range leaks {
			if bytes.Equal(leak.data, b) {
				return fmt.Errorf("archive breakout: file %q has been accessed via %q", leak.path, path)
			}
		}
		return nil
	})
}
