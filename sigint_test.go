// This test file is excluded from Windows builds entirely. The
// SIGINT subprocess test uses syscall.SysProcAttr{Setpgid: true},
// a Unix-only field; a runtime t.Skip("windows") inside the test
// isn't enough because the file is still compiled. Windows SIGINT
// coverage will be added in PR B1 using the Win32 console-signal
// API (GenerateConsoleCtrlEvent) and its own _windows_test.go file.

//go:build !windows

package main_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// tfdryBin builds the tfdry binary once for SIGINT-style subprocess tests
// and returns its path. The binary lives in a temporary directory that
// is deliberately leaked: t.Cleanup is per-test, but the binary is
// shared across tests via sync.Once, so removing it on the first test's
// cleanup would break subsequent callers. The OS reclaims /tmp on
// reboot, which is acceptable for a ~7 MB test artifact.
//
// Build output is captured so a build failure produces a helpful test
// error rather than a bare "exec: no such file" later.
//
// We can't share one binary across tests via TestMain because the build
// would happen even for "go test -short" runs that skip these tests.
// Each test that needs the binary calls this helper; the build cost is
// ~1-2s on modern hardware, amortised over a small number of subprocess
// tests.
var (
	tfdryBinOnce sync.Once
	tfdryBinPath string
	tfdryBinErr  error
)

func tfdryBin(t *testing.T) string {
	t.Helper()
	tfdryBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "tfdry-bin-*")
		if err != nil {
			tfdryBinErr = err
			return
		}
		// Leak the dir on test process exit — t.Cleanup is per-test so
		// can't safely remove a binary shared via sync.Once.
		bin := filepath.Join(dir, "tfdry")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, ".")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			tfdryBinErr = fmt.Errorf("go build: %w\n%s", err, out)
			return
		}
		tfdryBinPath = bin
	})
	if tfdryBinErr != nil {
		t.Fatalf("could not build tfdry binary: %v", tfdryBinErr)
	}
	return tfdryBinPath
}

// TestRunCLI_SIGINT_HandlesGracefully proves the end-to-end SIGINT
// contract that PR A2 introduces:
//
//   - main() wires signal.NotifyContext for SIGINT/SIGTERM.
//   - The derived ctx flows into every long-running checker entry point.
//   - On signal, the checker call returns context.Canceled, which run()
//     translates to "tfdry: interrupted" on stderr and exit code 130
//     (the canonical 128 + SIGINT).
//
// The cancellation unit tests in checker/context_test.go already prove
// the ctx.Err() propagation inside the checker package. This test fills
// in the remaining gap: that main() correctly bridges the OS signal into
// that ctx, end to end, via a real subprocess.
//
// We feed tfdry a directory containing many .tf files so the lint pass
// has enough work to still be running when we deliver the signal.
func TestRunCLI_SIGINT_HandlesGracefully(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows os.Interrupt to a child process is delivered via different APIs; covered separately when Windows CI lands in PR B1")
	}

	bin := tfdryBin(t)

	// Create enough files (with enough content per file) to keep the
	// parser busy for ~1s so the SIGINT lands reliably during the lint
	// pass rather than after it. tfdry is fast: 5000 tiny files can
	// complete in under 50ms on Apple Silicon. Empirical sweep showed
	// 4000 files × 50 locals each + 500ms pre-signal sleep gives 10/10
	// reliable interrupt-mid-work behaviour. The exact content is
	// irrelevant — we only need parser work, not specific check
	// outputs.
	dir := t.TempDir()
	const fileCount = 4000
	const localsPerFile = 50
	for i := 0; i < fileCount; i++ {
		path := filepath.Join(dir, fmt.Sprintf("f%05d.tf", i))
		var b strings.Builder
		b.WriteString("locals {\n")
		for j := 0; j < localsPerFile; j++ {
			fmt.Fprintf(&b, "  x_%d_%d = \"value-%d-%d\"\n", i, j, i, j)
		}
		b.WriteString("}\n")
		if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, dir)
	stderr := new(strings.Builder)
	cmd.Stderr = stderr
	cmd.Stdout = nil // discard
	// Put the child in its own process group so our SIGINT goes to the
	// tfdry process specifically, not the test harness.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	// Give the binary enough time to (a) finish Go runtime startup,
	// (b) call signal.NotifyContext, and (c) enter the parse loop.
	// Empirical sweep on Apple Silicon: 100ms is occasionally racy
	// against runtime startup (1/10 trials), 200ms is 10/10 reliable
	// locally. We use 500ms here as CI-headroom: GitHub Actions
	// runners can be 2-3× slower than a developer laptop on
	// process-startup-bound workloads, and the consequence of an
	// under-budget sleep is the process exiting with -1 (terminated
	// by signal during Go startup) instead of 130, which causes
	// false-positive test failures. The extra 300ms is paid once
	// per test invocation and is invisible to a developer running
	// the full suite locally.
	//
	// If scheduling delays the process so much that work completes
	// before the signal arrives, the process would exit 0 and the
	// assertion below would correctly catch the false positive.
	//
	// Future: replace the time-based handshake with a structured
	// "ready" signal from the binary (e.g., a stderr line read by
	// the parent, or a Unix-socket ping). Tracked alongside PR B1's
	// Windows SIGINT coverage.
	time.Sleep(500 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("cmd.Process.Signal: %v", err)
	}

	err := cmd.Wait()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected exec.ExitError, got err=%v (cmd exited normally? stderr=%q)", err, stderr.String())
	}
	if got := exitErr.ExitCode(); got != 130 {
		t.Errorf("exit code = %d, want 130 (SIGINT). stderr:\n%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "tfdry: interrupted") {
		t.Errorf("stderr should contain 'tfdry: interrupted'; got:\n%s", stderr.String())
	}
}
