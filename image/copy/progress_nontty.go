package copy

import (
	"fmt"
	"io"
	"time"

	"github.com/vbauerster/mpb/v8/decor"
	"go.podman.io/image/v5/types"
)

const (
	// nonTTYProgressChannelSize is the buffer size for the progress channel
	// in non-TTY mode. Buffered to prevent blocking during parallel downloads.
	nonTTYProgressChannelSize = 10

	// nonTTYProgressInterval is how often aggregate progress is printed
	// in non-TTY mode.
	nonTTYProgressInterval = 500 * time.Millisecond
)

// nonTTYProgressWriter consumes ProgressProperties from a channel and writes
// aggregate text-based progress output suitable for non-TTY environments.
// No mutex needed - single goroutine processes events sequentially from channel.
type nonTTYProgressWriter struct {
	output io.Writer

	// Aggregate tracking (no per-blob state needed)
	totalSize  int64 // Sum of all known blob sizes
	downloaded int64 // Total bytes downloaded (accumulated from OffsetUpdate)

	// Output throttling
	lastOutput      time.Time
	outputInterval  time.Duration
	progressChannel <-chan types.ProgressProperties
}

// newNonTTYProgressWriter creates a writer that outputs aggregate download
// progress as simple text lines, suitable for non-TTY environments like
// CI/CD pipelines or redirected output.
func newNonTTYProgressWriter(output io.Writer, interval time.Duration, pch chan types.ProgressProperties) *nonTTYProgressWriter {
	return &nonTTYProgressWriter{
		output:          output,
		outputInterval:  interval,
		progressChannel: pch,
	}
}

// setupNonTTYProgress configures text-based progress output for non-TTY
// environments unless the caller already provided a buffered Progress channel.
// It relies on the idea that options. Progress channel is only used once, to track progress with a progress bar
// Otherwise we must do some sort of fan-out
func setupNonTTYProgress(reportWriter io.Writer, options *Options) {
	// // Use user's interval if greater than our default, otherwise use default.
	// // This allows users to slow down output while maintaining a sensible minimum.
	// interval := max(options.ProgressInterval, nonTTYProgressInterval)
	// if options.ProgressInterval <= 0 {
	// 	options.ProgressInterval = nonTTYProgressInterval
	// }

	// if options.Progress == nil || cap(options.Progress) == 0 {
	// 	options.Progress = make(chan types.ProgressProperties, nonTTYProgressChannelSize)
	// }

	pw := newNonTTYProgressWriter(reportWriter, options.ProgressInterval, options.Progress)
	go pw.Run()
}

// Run consumes progress events from the channel and prints throttled
// aggregate progress. Blocks until the channel is closed. Intended to
// be called as a goroutine: go tw.Run(progressChan)
func (w *nonTTYProgressWriter) Run() {
	for props := range w.progressChannel {
		switch props.Event {
		case types.ProgressEventNewArtifact:
			// New blob starting - add its size to total
			w.totalSize += props.Artifact.Size

		case types.ProgressEventRead:
			// Bytes downloaded - accumulate and maybe print
			w.downloaded += int64(props.OffsetUpdate)
			if time.Since(w.lastOutput) > w.outputInterval {
				fmt.Fprintf(w.output, "Progress: %.1f / %.1f\n",
					decor.SizeB1024(w.downloaded), decor.SizeB1024(w.totalSize))
				w.lastOutput = time.Now()
			}
		}
	}
}
