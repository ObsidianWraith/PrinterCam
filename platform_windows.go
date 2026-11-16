//go:build windows

package main

import (
	"errors"
	"os/exec"
	"syscall"
	"unsafe"
)

type blob struct {
	Size uint32
	Data *byte
}

var crypt = syscall.NewLazyDLL("crypt32.dll")
var kernel = syscall.NewLazyDLL("kernel32.dll")

func cryptData(data []byte, decrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty settings")
	}
	input := blob{uint32(len(data)), &data[0]}
	var output blob
	name := "CryptProtectData"
	if decrypt {
		name = "CryptUnprotectData"
	}
	ok, _, _ := crypt.NewProc(name).Call(uintptr(unsafe.Pointer(&input)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&output)))
	if ok == 0 {
		return nil, errors.New("Windows could not protect camera settings")
	}
	defer kernel.NewProc("LocalFree").Call(uintptr(unsafe.Pointer(output.Data)))
	return append([]byte(nil), unsafe.Slice(output.Data, output.Size)...), nil
}
func protect(b []byte) ([]byte, error)   { return cryptData(b, false) }
func unprotect(b []byte) ([]byte, error) { return cryptData(b, true) }
func replaceFile(from, to string) error {
	a, e := syscall.UTF16PtrFromString(from)
	if e != nil {
		return e
	}
	b, e := syscall.UTF16PtrFromString(to)
	if e != nil {
		return e
	}
	ok, _, err := kernel.NewProc("MoveFileExW").Call(uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(b)), 0x1|0x8)
	if ok == 0 {
		return err
	}
	return nil
}
func quietProcess(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
func openBrowser(url string) {
	c := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	quietProcess(c)
	c.Start()
}
func bridgeName() string { return "go2rtc.exe" }
