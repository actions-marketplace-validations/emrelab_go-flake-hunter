//go:build darwin || linux || windows

package gotest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// The deadline is delivered after the complete tree acknowledges readiness.
// This exercises DeadlineExceeded without racing process startup against a timer.
type commandDeadline struct {
	context.Context
	expired chan struct{}
}

func (c *commandDeadline) Done() <-chan struct{} { return c.expired }
func (c *commandDeadline) Err() error {
	select {
	case <-c.expired:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func TestCancellationStopsDescendants(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	testCommandTree(t, ctx, cancel, false, nil)
}

func TestDeadlineStopsDescendants(t *testing.T) {
	ctx := &commandDeadline{Context: context.Background(), expired: make(chan struct{})}
	testCommandTree(t, ctx, func() { close(ctx.expired) }, false, nil)
}

// A live TCP connection belongs only to its helper process. EOF after cancellation
// proves termination without a delayed write or polling the reuse-prone PID.
func testCommandTree(t *testing.T, ctx context.Context, cancel func(), normalExit bool, configure func(any)) {
	t.Helper()
	if testing.Short() {
		t.Skip("real subprocess cancellation")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := commandContext(ctx, exe, "-test.run=^TestCancellationHelper$")
	cmd.Env = append(os.Environ(), "GFH_CANCEL_HELPER=driver", "GFH_CANCEL_ADDRESS="+listener.Addr().String())
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if configure != nil {
		configure(cmd)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() { once.Do(cancel) }
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("command failed to finish cleanup")
		}
	}()
	readers := make([]*bufio.Reader, 0, 3)
	var driver net.Conn
	for range 3 {
		conn, err := listener.Accept()
		if err != nil {
			t.Fatalf("descendant readiness: %v", err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "ready ") {
			t.Fatalf("descendant handshake: %q %v", line, err)
		}
		if strings.HasPrefix(line, "ready driver") {
			driver = conn
		}
		readers = append(readers, reader)
	}
	if normalExit {
		if driver == nil {
			t.Fatal("driver did not acknowledge readiness")
		}
		if _, err := driver.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
	} else {
		stop()
	}
	for _, reader := range readers {
		if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
			t.Errorf("descendant survived cancellation: %v", err)
		}
	}
	select {
	case <-done:
		if waitErr == nil {
			t.Error("cancelled command returned success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled command did not return")
	}
}

func TestCancellationHelper(t *testing.T) {
	role := os.Getenv("GFH_CANCEL_HELPER")
	if role == "" {
		return
	}
	if role == "exit" {
		return
	}
	if role != "grandchild" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		next := "child"
		if role == "child" {
			next = "grandchild"
		}
		cmd := exec.Command(exe, "-test.run=^TestCancellationHelper$")
		cmd.Env = append(os.Environ(), "GFH_CANCEL_HELPER="+next)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if role != "driver-exit" {
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
		}
	}
	conn, err := net.DialTimeout("tcp", os.Getenv("GFH_CANCEL_ADDRESS"), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := fmt.Fprintf(conn, "ready %s %d\n", role, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	// Only driver-exit receives a release byte after the complete tree is ready.
	var probe [1]byte
	_, _ = conn.Read(probe[:])
}
