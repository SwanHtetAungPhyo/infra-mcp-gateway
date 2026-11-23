package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

const (
	maxStdout = 64 << 20 // 64 MiB
	maxStderr = 1 << 20  // 1 MiB
)

// ErrOutputTooLarge is returned when a command prints more than maxStdout.
var ErrOutputTooLarge = errors.New("command output exceeded limit")

// slots limits how many commands run at once across all tools.
var slots = make(chan struct{}, scanSlots())

func scanSlots() int {
	if n, err := strconv.Atoi(os.Getenv("MAX_CONCURRENT_SCANS")); err == nil && n > 0 {
		return n
	}
	return 2
}

// limitedBuffer keeps the first max bytes and drops the rest, but keeps
// accepting writes so the child process never blocks on a full pipe.
type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); len(p) > room {
		b.truncated = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

// RunCommand runs name with args and returns its stdout and stderr.
// It waits for a free slot, caps captured output, and on cancel kills the
// command together with every process it started.
func RunCommand(ctx context.Context, name string, args ...string) (string, string, error) {
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return "", "", fmt.Errorf("waiting for a free scan slot: %w", ctx.Err())
	}

	cmd := exec.CommandContext(ctx, name, args...)
	// Own process group, so cancel kills the command and everything it started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// After cancel, wait at most 5s for output pipes to close.
	cmd.WaitDelay = 5 * time.Second

	stdout := &limitedBuffer{max: maxStdout}
	stderr := &limitedBuffer{max: maxStderr}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if err == nil && stdout.truncated {
		err = fmt.Errorf("%s: %w (%d bytes)", name, ErrOutputTooLarge, maxStdout)
	}
	return stdout.buf.String(), stderr.buf.String(), err
}
