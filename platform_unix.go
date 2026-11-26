//go:build linux || darwin

package main

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

func addressInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}

func freeBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return int64(min(uint64(stat.Bavail)*uint64(stat.Bsize), uint64(math.MaxInt64))), nil
}

func lockRoom(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errRoomInUse
		}
		return nil, err
	}
	return f, nil
}

func openBrowser(url string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	return exec.Command(command, url).Run()
}

func showStartupError(string) {}
