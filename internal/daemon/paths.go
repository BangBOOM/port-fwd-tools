//go:build darwin || linux

package daemon

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type Paths struct{ Home, Config, Runtime, Socket, Log string }

func ResolvePaths(home string) (Paths, error) {
	if home == "" {
		home = os.Getenv("PF_HOME")
	}
	if home == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, err
		}
		home = filepath.Join(base, "port-fwd-tools")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return Paths{}, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return Paths{}, err
	}
	// macOS Unix socket paths are short. Hashing also supports long --home paths.
	digest := sha256.Sum256([]byte(abs))
	runtime := filepath.Join("/tmp", fmt.Sprintf("pf-%d-%x", os.Getuid(), digest[:8]))
	if err = os.Mkdir(runtime, 0700); err != nil && !os.IsExist(err) {
		return Paths{}, err
	}
	st, err := os.Lstat(runtime)
	if err != nil {
		return Paths{}, err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || st.Mode().Perm() != 0700 || !ok || owner.Uid != uint32(os.Getuid()) {
		return Paths{}, fmt.Errorf("runtime directory must be owned by this user with mode 0700: %s", runtime)
	}
	return Paths{Home: abs, Config: filepath.Join(abs, "config.toml"), Runtime: runtime, Socket: filepath.Join(runtime, "daemon.sock"), Log: filepath.Join(abs, "daemon.log")}, nil
}

func lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another daemon already owns this config: %w", err)
	}
	return f, nil
}
