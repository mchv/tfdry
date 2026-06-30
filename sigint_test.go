// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

// This test file is excluded from non-Unix builds entirely. The
// SIGINT subprocess test uses syscall.SysProcAttr{Setpgid: true}
// and syscall.Kill(-pid, SIGINT), both of which are Unix-only API
// surface — a runtime t.Skip inside the test isn't enough because
// the file is still compiled. The `unix` build tag (Linux, macOS,
// BSD, illumos, AIX) matches the actual platform constraint;
// using `!windows` would have let the file attempt to compile on
// Plan 9 and JS/wasm where it would also fail.
//
// Windows SIGINT coverage will be added in PR B1 using the Win32
// console-signal API (GenerateConsoleCtrlEvent) and its own
// _windows_test.go file.

//go:build unix

package main_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// readyMarker is the stable test-only stderr line that the binary emits
// after signal.NotifyContext arms its handlers (gated by TFDRY_TEST_READY=1
// in the environment, see main.go). The trailing newline is part of the
// marker — used as a frame delimiter so partial reads can't false-match
// on a substring like "tfdry: test-ready-something-else".
const readyMarker = "tfdry: test-ready\n"

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
		// No ".exe" suffix needed: this file is //go:build unix, so
		// runtime.GOOS is always one of linux/darwin/bsd/illumos/aix.
		bin := filepath.Join(dir, "tfdry")
		// exec.CommandContext (not exec.Command) to satisfy the noctx
		// linter; we don't actually want to cancel this build mid-flight
		// (it's a one-shot setup helper), so context.Background is
		// appropriate. The go build itself is fast (~1-2s); a stuck
		// build would manifest as the whole test timing out via
		// `go test -timeout`, not a missing cancellation point here.
		cmd := exec.CommandContext(context.Background(), "go", "build", "-o", bin, ".")
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
	// No need for an in-test runtime.GOOS=="windows" Skip — the file
	// has a //go:build unix constraint at the top, so this test only
	// compiles on Unix-likes in the first place. Windows SIGINT
	// coverage will land in PR B1 with its own _windows_test.go.

	bin := tfdryBin(t)

	// Create enough work to keep the parser busy past the pre-signal
	// sleep so SIGINT lands during the lint pass rather than after it.
	// tfdry is fast: small workloads finish in under 50ms on Apple
	// Silicon. Empirical sweep with the 500ms sleep below:
	//
	//      total locals    stability
	//        100 000           0/10  (Gemini's first suggestion — too small)
	//        200 000           5/5
	//        500 000          10/10  (current — chosen for CI headroom)
	//
	// Settled on 50 files × 10 000 locals each = 500 000 total locals.
	// Trade-offs vs the earlier 4000-files × 50-locals setup:
	//
	//   - Disk I/O: 80× fewer file-system calls (50 writes vs 4000).
	//     The earlier shape was disk-bound on CI shared runners.
	//   - Memory: ~400KB per HCL file × 50 = ~20MB resident. Fine.
	//   - Cancellation granularity: 50 per-file checkpoints in
	//     ParseDir vs 4000 — still high enough to land the SIGINT
	//     mid-walk, but the larger per-file work means each
	//     checkpoint gap is ~10ms (still tighter than the 500ms
	//     sleep budget).
	//
	// The exact content is irrelevant — we only need parser work,
	// not specific check outputs.
	dir := t.TempDir()
	const fileCount = 50
	const localsPerFile = 10000
	for i := 0; i < fileCount; i++ {
		path := filepath.Join(dir, fmt.Sprintf("f%05d.tf", i))
		var b strings.Builder
		b.WriteString("locals {\n")
		for j := 0; j < localsPerFile; j++ {
			fmt.Fprintf(&b, "  x_%d_%d = \"value-%d-%d\"\n", i, j, i, j)
		}
		b.WriteString("}\n")
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, dir)
	// Append TFDRY_TEST_READY=1 so the binary emits a "tfdry: test-ready"
	// line on stderr immediately after signal.NotifyContext arms its
	// SIGINT handlers. We block until that line arrives before delivering
	// SIGINT, eliminating the previous 500ms timing-based handshake and
	// its flakiness on slow CI runners.
	cmd.Env = append(os.Environ(), "TFDRY_TEST_READY=1")
	stderr := new(strings.Builder)
	// We need to read stderr in real time (to detect the ready marker
	// before sending the signal) AND keep the full transcript for the
	// post-Wait assertions. The classic pattern: pipe + tee through a
	// goroutine that copies into the Builder while we Scan for the
	// marker.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("cmd.StderrPipe: %v", err)
	}
	cmd.Stdout = nil // discard
	// Put the child in its own process group (PGID = child PID).
	// Setpgid:true affects which signals reach the child, not which
	// signals reach the harness: with the child in its own group, a
	// terminal-driven SIGINT delivered to the test harness's foreground
	// process group (e.g. when a developer hits Ctrl-C during `go test`)
	// does NOT cascade to the child. It also makes the negative-PID
	// kill below meaningful — `syscall.Kill(-pid, sig)` sends to the
	// whole group, so any future subprocess that tfdry spawns (LSP
	// child, watch-mode helpers, etc.) would also receive the signal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	// Wait for the ready marker on stderr. We use a goroutine to
	// stream stderr into the Builder concurrently — if we read the
	// pipe ourselves, lines after the marker (specifically the
	// post-SIGINT "tfdry: interrupted" line) would be lost.
	//
	// Synchronisation contract (important — *strings.Builder is NOT
	// goroutine-safe):
	//   - Scanner goroutine has EXCLUSIVE write access to `stderr`
	//     while it's running.
	//   - Main goroutine must NOT read `stderr.String()` until the
	//     scanner has fully exited. We use TWO channels for this:
	//       * `scanErr` (buffered): carries an error value if the
	//         scanner observed one. Send happens-before any receive,
	//         so the value is delivered reliably, but receiving from
	//         a buffered channel only synchronises with the SEND,
	//         not with the goroutine's exit — the sender may still
	//         be executing code after the send.
	//       * `done` (unbuffered, closed via `defer`): closed AFTER
	//         all of the goroutine's deferred statements have run.
	//         `<-done` is a strict happens-after barrier for the
	//         goroutine's full exit — every prior stderr.Write call
	//         is provably visible after this receive.
	//   - Every site that reads `stderr.String()` from the main
	//     goroutine first does `<-done` to wait for full scanner
	//     exit. The pattern below routes every failure path through
	//     this barrier.
	//
	// Failure modes and how each is handled:
	//   A. EOF before marker — scanner sends to scanErr, then closes
	//      `ready` so main wakes up; main does a non-blocking receive
	//      on scanErr, observes the error, kills + waits the
	//      subprocess, then joins via `<-done` before reading stderr.
	//   B. Marker observed, then SIGINT delivery and graceful exit —
	//      happy path. Scanner keeps running until subprocess exits;
	//      main's post-Wait `<-done` joins.
	//   C. 10s timeout with no marker — main kills the subprocess
	//      (forcing stderrPipe EOF), waits for the process, then
	//      `<-done` joins before reading stderr for diagnostics.
	//
	// Timeout budget: 10s. On the slowest CI runners observed, Go
	// runtime startup + first stderr flush completes well under 2s,
	// so a 10s cap is safe headroom that still fails fast if the
	// binary is deadlocked or panicked before reaching the marker.
	ready := make(chan struct{})
	scanErr := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		// LIFO defer order: close(scanErr) runs first, then
		// close(done). So `<-done` is a strict "scanner has fully
		// exited; scanErr is closed; no further stderr writes" barrier.
		defer close(done)
		defer close(scanErr)
		buf := make([]byte, 4096)
		var seen bool
		// markerBuf is a bounded rolling window over the most recently
		// read bytes — large enough to detect the marker even when it
		// straddles a chunk boundary. We do NOT scan the full
		// accumulated `stderr` builder on every chunk: that's O(N²)
		// over total bytes and unnecessary, since the marker arrives
		// within the first few stderr reads in practice. Once the
		// marker is observed (`seen=true`), we drop markerBuf
		// entirely and stop scanning.
		//
		// Trim policy: keep the trailing (len(readyMarker)-1) bytes
		// after each non-matching scan so a marker that crosses the
		// next read boundary is still detectable. Memory ceiling per
		// goroutine: ~4 KiB (one chunk) + 17 bytes (tail).
		var markerBuf []byte

		for {
			n, err := stderrPipe.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				stderr.Write(chunk)
				if !seen {
					markerBuf = append(markerBuf, chunk...)
					if bytes.Contains(markerBuf, []byte(readyMarker)) {
						seen = true
						close(ready)
						markerBuf = nil // free; further scanning is unnecessary
					} else if len(markerBuf) >= len(readyMarker) {
						// Keep only enough trailing bytes to detect a
						// marker that straddles the next read boundary.
						markerBuf = markerBuf[len(markerBuf)-(len(readyMarker)-1):]
					}
				}
			}
			if err != nil {
				switch {
				case errors.Is(err, io.EOF):
					// Normal pipe close: subprocess exited. If we
					// never saw the marker, the binary completed all
					// its work or crashed before reaching the
					// notify-ready point — surface as failure.
					if !seen {
						scanErr <- fmt.Errorf("stderr closed before ready marker: %w", err)
						close(ready)
					}
					return
				default:
					// Anomalous read error (broken pipe, I/O failure,
					// context cancellation propagated to the pipe).
					// Report regardless of `seen`. Without this, a
					// post-marker read error would be silently
					// swallowed, and the post-Wait drain on
					// `<-scanErr` would receive nil — letting the
					// test continue with a partial stderr transcript.
					scanErr <- fmt.Errorf("stderr read error (seen=%v): %w", seen, err)
					if !seen {
						close(ready)
					}
					return
				}
			}
		}
	}()

	select {
	case <-ready:
		// `ready` was closed either by the marker-observation path
		// (happy) or by the EOF-before-marker error path. In the
		// error case, scanErr received the error BEFORE ready was
		// closed (goroutine ordering above), so a non-blocking
		// receive on scanErr will produce the value if and only if
		// the error path ran. In the happy path, scanner is still
		// running and the receive falls into `default`.
		select {
		case err, ok := <-scanErr:
			if ok && err != nil {
				// Error path: kill + wait the subprocess to drain
				// the pipe, then strict-join via `<-done` so the
				// scanner has fully exited before reading stderr.
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				<-done
				t.Fatalf("scanner failed before ready marker: %v\nstderr:\n%s", err, stderr.String())
			}
			// ok=false means scanErr was already closed without an
			// error sent. This can happen if subprocess finished its
			// work AND its stderr pipe drained between marker
			// observation and our check — very fast happy path.
			// Continue; the SIGINT delivery below will surface any
			// real failure (subprocess already exited).
		default:
			// Scanner still running, happy path. Proceed to SIGINT.
		}
	case <-time.After(10 * time.Second):
		// Kill the subprocess so stderrPipe EOFs, then Wait so the
		// subprocess is fully gone. `<-done` strict-joins the
		// scanner goroutine (waits for `defer close(done)` to run,
		// which happens AFTER `defer close(scanErr)` due to LIFO
		// defer order). Only after the join is it safe to read
		// `stderr.String()`.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		<-done
		t.Fatalf("subprocess did not emit ready marker within 10s; stderr:\n%s", stderr.String())
	}
	// Send to the entire process group via negative PID (works because
	// Setpgid:true above made the child a group leader with PGID = its
	// own PID). Two reasons this is preferable to cmd.Process.Signal:
	//
	//   1. Future-proofs the test for when tfdry spawns subprocesses
	//      (LSP child, watch-mode helpers, terraform subprocess wrap):
	//      they all share the child's PGID and will receive the signal.
	//      cmd.Process.Signal would only signal the immediate child.
	//   2. Makes the Setpgid:true above semantically meaningful for the
	//      kill path, not just the harness-isolation path.
	//
	// A non-nil error here means the subprocess exited before we got
	// to signal it — i.e., the workload finished before we could even
	// observe the ready marker + dispatch the kill. Wait for subprocess
	// + strict-join scanner via `<-done` before reading stderr.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		_ = cmd.Wait()
		<-done
		t.Fatalf("syscall.Kill(-%d, SIGINT): subprocess exited before signal could be delivered "+
			"(workload of %d files × %d locals too small for this machine?): %v\nstderr:\n%s",
			cmd.Process.Pid, fileCount, localsPerFile, err, stderr.String())
	}

	waitErr := cmd.Wait()
	// Strict join: wait for `defer close(done)` to run, which
	// happens AFTER the scanner's loop exit AND AFTER
	// `defer close(scanErr)`. After this barrier:
	//   - The scanner is provably done writing to stderr.
	//   - scanErr is provably closed; a non-blocking receive
	//     returns the buffered error (if any) or nil.
	// Reading `stderr.String()` and `<-scanErr` are both safe.
	<-done
	if err := <-scanErr; err != nil {
		t.Fatalf("scanner reported error after subprocess exit: %v\nstderr:\n%s", err, stderr.String())
	}

	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("expected exec.ExitError, got err=%v (cmd exited normally? stderr=%q)", waitErr, stderr.String())
	}
	if got := exitErr.ExitCode(); got != 130 {
		t.Errorf("exit code = %d, want 130 (SIGINT). stderr:\n%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "tfdry: interrupted") {
		t.Errorf("stderr should contain 'tfdry: interrupted'; got:\n%s", stderr.String())
	}
}
