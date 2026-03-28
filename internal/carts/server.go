package carts

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	pidFileName  = "dolt-server.pid"
	portFileName = "dolt-server.port"
)

// EnsureServer ensures a dolt sql-server is running for the carts database.
// If a server is already running (PID file exists and process alive), it returns the port.
// Otherwise it starts a new server on an ephemeral port.
func EnsureServer(cartsDir string) (int, error) {
	// Check if already running
	if port, err := runningServerPort(cartsDir); err == nil {
		return port, nil
	}

	// Start new server
	return startServer(cartsDir)
}

// runningServerPort returns the port of a running server, or error if not running.
func runningServerPort(cartsDir string) (int, error) {
	pidData, err := os.ReadFile(filepath.Join(cartsDir, pidFileName))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return 0, err
	}
	// Check if process is alive
	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0, err
	}
	// Signal 0 checks if process exists
	if err := proc.Signal(os.Signal(nil)); err != nil {
		return 0, fmt.Errorf("server process %d not running: %w", pid, err)
	}

	portData, err := os.ReadFile(filepath.Join(cartsDir, portFileName))
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(portData)))
	if err != nil {
		return 0, err
	}
	return port, nil
}

// startServer starts a dolt sql-server on an ephemeral port.
func startServer(cartsDir string) (int, error) {
	doltDir := filepath.Join(cartsDir, "dolt")

	// Ensure dolt directory exists and is initialized
	if err := ensureDoltInit(doltDir); err != nil {
		return 0, fmt.Errorf("init dolt: %w", err)
	}

	// Allocate ephemeral port
	port, err := allocateEphemeralPort("127.0.0.1")
	if err != nil {
		return 0, err
	}

	// Start dolt sql-server
	logFile, err := os.Create(filepath.Join(cartsDir, "dolt-server.log"))
	if err != nil {
		return 0, fmt.Errorf("create log file: %w", err)
	}

	cmd := exec.Command("dolt", "sql-server",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--no-auto-commit",
	)
	cmd.Dir = doltDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return 0, fmt.Errorf("start dolt sql-server: %w", err)
	}
	logFile.Close()

	// Write state files
	if err := os.WriteFile(filepath.Join(cartsDir, pidFileName), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(cartsDir, portFileName), []byte(strconv.Itoa(port)), 0o644); err != nil {
		return 0, err
	}

	// Wait for server to be ready
	if err := waitForServer("127.0.0.1", port, 30*time.Second); err != nil {
		return 0, fmt.Errorf("server failed to start: %w", err)
	}

	return port, nil
}

// ensureDoltInit ensures the dolt directory is initialized.
func ensureDoltInit(doltDir string) error {
	if err := os.MkdirAll(doltDir, 0o750); err != nil {
		return err
	}
	// Check if already initialized
	if _, err := os.Stat(filepath.Join(doltDir, ".dolt")); err == nil {
		return nil
	}
	cmd := exec.Command("dolt", "init")
	cmd.Dir = doltDir
	cmd.Env = append(os.Environ(),
		"DOLT_SILENCE_USER_REQ_FOR_TESTING=Y",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("dolt init: %s: %w", string(out), err)
	}
	return nil
}

// allocateEphemeralPort asks the OS for a free TCP port.
func allocateEphemeralPort(host string) (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, fmt.Errorf("allocating ephemeral port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// waitForServer polls until the server accepts connections.
func waitForServer(host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	dsn := fmt.Sprintf("root@tcp(%s:%d)/", host, port)
	for time.Now().Before(deadline) {
		db, err := sql.Open("mysql", dsn)
		if err == nil {
			if err := db.Ping(); err == nil {
				db.Close()
				return nil
			}
			db.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for dolt server on port %d", port)
}

// StopServer stops a running dolt server for the given carts directory.
func StopServer(cartsDir string) error {
	pidData, err := os.ReadFile(filepath.Join(cartsDir, pidFileName))
	if err != nil {
		return nil // no server running
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	_ = proc.Signal(os.Interrupt)
	// Clean up state files
	os.Remove(filepath.Join(cartsDir, pidFileName))
	os.Remove(filepath.Join(cartsDir, portFileName))
	return nil
}
