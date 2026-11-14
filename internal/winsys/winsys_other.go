//go:build !windows

// Package winsys locates Windows' own programs by full path (see the
// Windows file). Elsewhere it returns the name unchanged, for tests.
package winsys

// Exe returns name unchanged off Windows.
func Exe(name string) string { return name }
