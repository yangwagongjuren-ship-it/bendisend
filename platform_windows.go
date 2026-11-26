package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"syscall"
	"unsafe"
)

var diskFreeSpace = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func addressInUse(err error) bool {
	const wsaAddressInUse = syscall.Errno(10048)
	return errors.Is(err, wsaAddressInUse)
}

func freeBytes(path string) (int64, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free uint64
	result, _, err := diskFreeSpace.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&free)), 0, 0)
	if result == 0 {
		return 0, err
	}
	return int64(min(free, uint64(math.MaxInt64))), nil
}

func lockRoom(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		const sharingViolation = syscall.Errno(32)
		if errors.Is(err, sharingViolation) {
			return nil, errRoomInUse
		}
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func openBrowser(url string) error {
	open := syscall.StringToUTF16Ptr("open")
	address := syscall.StringToUTF16Ptr(url)
	proc := syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW")
	result, _, err := proc.Call(0, uintptr(unsafe.Pointer(open)), uintptr(unsafe.Pointer(address)), 0, 0, 1)
	if result <= 32 {
		return fmt.Errorf("ShellExecuteW returned %d: %w", result, err)
	}
	return nil
}

func showStartupError(message string) {
	body := syscall.StringToUTF16Ptr(message)
	title := syscall.StringToUTF16Ptr("本地传 · BendiSend")
	proc := syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")
	proc.Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), 0x10)
}
