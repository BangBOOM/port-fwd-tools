//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type response struct {
	State Snapshot `json:"state"`
	Error string   `json:"error,omitempty"`
}

func Serve(paths Paths) error {
	guard, err := lock(filepath.Join(paths.Home, "daemon.lock"))
	if err != nil {
		return err
	}
	defer guard.Close()
	if _, err = exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("OpenSSH client is required: %w", err)
	}
	manager, err := NewManager(paths, Options{})
	if err != nil {
		return err
	}
	defer manager.Close()
	_ = os.Remove(paths.Socket)
	listener, err := net.Listen("unix", paths.Socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(paths.Socket)
	if err = os.Chmod(paths.Socket, 0600); err != nil {
		return err
	}
	pidPath := filepath.Join(paths.Home, "daemon.pid")
	if err = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0600); err != nil {
		return err
	}
	defer os.Remove(pidPath)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rpc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req Request
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(response{Error: err.Error()})
			return
		}
		if req.Action == "shutdown" {
			_ = json.NewEncoder(w).Encode(response{State: manager.Snapshot()})
			cancel()
			return
		}
		if err := manager.Apply(req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(response{Error: err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(response{State: manager.Snapshot()})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	serveError := make(chan error, 1)
	go func() { serveError <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
	case err = <-serveError:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	err = server.Shutdown(shutdownCtx)
	manager.Close()
	return err
}

func Ensure(paths Paths) error {
	if _, err := NewClient(paths).Do(Request{Action: "state"}); err == nil {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if info, err := os.Stat(paths.Log); err == nil && info.Size() > 1<<20 {
		_ = os.Rename(paths.Log, paths.Log+".1")
	}
	output, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, "--home", paths.Home, "daemon", "run")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = output, output
	if err = cmd.Start(); err != nil {
		output.Close()
		return err
	}
	output.Close()
	_ = cmd.Process.Release()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = NewClient(paths).Do(Request{Action: "state"}); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not start; inspect %s", paths.Log)
}
