//go:build !windows

package main

import (
	"os"
	"os/exec"
	"runtime"
)

func protect(b []byte) ([]byte, error)   { return b, nil }
func unprotect(b []byte) ([]byte, error) { return b, nil }
func replaceFile(from, to string) error  { return os.Rename(from, to) }
func quietProcess(c *exec.Cmd)           {}
func openBrowser(url string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	exec.Command(name, url).Start()
}
func bridgeName() string { return "go2rtc" }
