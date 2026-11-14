//go:build race

package internal_test

// Under the race detector the userspace network stack inside WireGuard
// (gVisor sleeper) spins instead of sleeping (measured: 745% CPU against 3%),
// so these tests run over plain TLS then. The tunnel is covered without
// -race and by the wgtun package.
const testWGMode = "off"
