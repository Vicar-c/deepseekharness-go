package session_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/session"
)

// The helper process patterns below run this test binary again and let the lock
// be exercised by a real second process, so the test covers the kernel lock
// rather than the in-process map.

const (
	helperEnvMode = "DSH_SESSION_LOCK_HELPER"
	helperEnvPath = "DSH_SESSION_LOCK_PATH"
)

func TestSessionLockHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnvMode)
	if mode == "" {
		t.Skip("helper process only")
	}
	writer, err := session.OpenWriter(os.Getenv(helperEnvPath))
	if err != nil {
		fmt.Printf("ERR %v\n", err)
		os.Exit(3)
	}
	fmt.Println("READY")
	switch mode {
	case "exit":
		// Simulate a crash: leave without Close, so only the OS releases the lock.
		os.Exit(0)
	default:
		// Hold the lock until the parent closes stdin.
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = writer.Close()
	}
	os.Exit(0)
}

type helperProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	output  *bufio.Reader
}

func startLockHelper(t *testing.T, mode, path string) *helperProcess {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=TestSessionLockHelperProcess", "-test.v=false")
	command.Env = append(os.Environ(), helperEnvMode+"="+mode, helperEnvPath+"="+path)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	reader := bufio.NewReader(stdout)
	if mode == "hold" {
		line, err := reader.ReadString('\n')
		if err != nil || !strings.Contains(line, "READY") {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("helper did not become ready: %q (%v)", line, err)
		}
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	return &helperProcess{command: command, stdin: stdin, output: reader}
}

func createClosedSession(t *testing.T, dir, id string) string {
	t.Helper()
	path := filepath.Join(dir, id+".jsonl")
	writer, err := session.CreateSessionFile(path, session.Header{ID: id, CreatedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatalf("CreateSessionFile: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path
}

// TestCrossProcessLockRejectsSecondWriter is the core of B-10: two processes must
// never append to one log.
func TestCrossProcessLockRejectsSecondWriter(t *testing.T) {
	dir := t.TempDir()
	id := "20261122-666666-aaaaaaaa"
	path := createClosedSession(t, dir, id)

	helper := startLockHelper(t, "hold", path)

	if _, err := session.OpenWriter(path); !errors.Is(err, session.ErrSessionLocked) {
		t.Fatalf("second writer error = %v, want ErrSessionLocked", err)
	}

	// Closing the helper's stdin makes it Close the writer and exit.
	_ = helper.stdin.Close()
	if err := helper.command.Wait(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}

	writer, err := openWithRetry(path)
	if err != nil {
		t.Fatalf("writer after helper exit: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestLockReleasedWhenProcessDies covers the crash case: the OS drops the lock
// when the owner exits without closing, so a killed harness never leaves a
// session permanently locked.
func TestLockReleasedWhenProcessDies(t *testing.T) {
	dir := t.TempDir()
	id := "20261122-777777-bbbbbbbb"
	path := createClosedSession(t, dir, id)

	helper := startLockHelper(t, "exit", path)
	if err := helper.command.Wait(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}

	writer, err := openWithRetry(path)
	if err != nil {
		t.Fatalf("writer after crash: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestResumeFailsWhenAnotherProcessOwnsTheLog keeps a resume from repairing a log
// that another process is writing.
func TestResumeFailsWhenAnotherProcessOwnsTheLog(t *testing.T) {
	dir := t.TempDir()
	id := "20261122-888888-cccccccc"
	path := createClosedSession(t, dir, id)
	helper := startLockHelper(t, "hold", path)
	_ = helper

	store := session.NewStore(dir)
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Resume(id); !errors.Is(err, session.ErrSessionLocked) {
		t.Fatalf("Resume error = %v, want ErrSessionLocked", err)
	}
}

func openWithRetry(path string) (*session.Writer, error) {
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		writer, err := session.OpenWriter(path)
		if err == nil {
			return writer, nil
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	return nil, lastErr
}

// TestFlushCountsSuccessfulSyncs pins the durability checkpoint used by the loop.
func TestFlushCountsSuccessfulSyncs(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create("flush")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.AppendEvent(sess, "turn/start", session.TurnMarker{Turn: 1}, nil, nil); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := store.SyncCount(sess.ID); got != 0 {
		t.Fatalf("syncs before flush = %d", got)
	}
	if err := store.Flush(sess); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := store.SyncCount(sess.ID); got != 1 {
		t.Fatalf("syncs after flush = %d, want 1", got)
	}
	if err := store.Flush(nil); err == nil {
		t.Fatal("Flush(nil) should fail")
	}
}
