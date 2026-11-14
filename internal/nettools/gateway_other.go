//go:build !windows

package nettools

// Gateways off Windows: not reported.
func Gateways() []string { return nil }
