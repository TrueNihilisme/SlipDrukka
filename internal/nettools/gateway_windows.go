package nettools

import (
	"strings"

	"slipdrukka/internal/winps"
)

// Gateways returns the routers of the default routes (IPv4).
func Gateways() []string {
	out, err := winps.Run(`@(Get-NetRoute -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | ForEach-Object { [string]$_.NextHop } | Sort-Object -Unique) -join ','`, nil)
	if err != nil {
		return nil
	}
	var list []string
	for _, g := range strings.Split(strings.TrimSpace(string(out)), ",") {
		if g != "" && g != "0.0.0.0" {
			list = append(list, g)
		}
	}
	return list
}
