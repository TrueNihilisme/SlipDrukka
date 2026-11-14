package nettools

import "testing"

func TestParseTrace(t *testing.T) {
	win := "\r\nTracing route to 203.0.113.9 over a maximum of 20 hops\r\n\r\n" +
		"  1    <1 ms    <1 ms    <1 ms  192.168.1.1 \r\n" +
		"  2    12 ms    11 ms    13 ms  10.10.1.4 \r\n" +
		"  3     *        *        *     Request timed out.\r\n" +
		"  4    25 ms     *       24 ms  203.0.113.9 \r\n\r\nTrace complete.\r\n"
	// Ukrainian Windows: the same digits, other words (and an OEM code page).
	uk := "\r\nТрасування маршруту до 203.0.113.9 (не більше 20 переходів)\r\n" +
		"  1    <1 мс    <1 мс    <1 мс  192.168.1.1\r\n  2     *        *        *     Перевищено інтервал очікування.\r\n"
	lin := "traceroute to 203.0.113.9 (203.0.113.9), 20 hops max\n 1  192.168.1.1  0.512 ms  0.433 ms  0.401 ms\n 2  * * *\n 3  203.0.113.9  24.1 ms  23.9 ms  24.0 ms\n"
	h := ParseTrace([]byte(win))
	if len(h) != 4 || h[0].IP != "192.168.1.1" || h[0].MS[0] != 0.5 || h[1].MS[2] != 13 || h[2].IP != "" || h[2].MS[0] != -1 || h[3].MS[1] != -1 || h[3].IP != "203.0.113.9" {
		t.Fatalf("windows: %+v", h)
	}
	if h = ParseTrace([]byte(uk)); len(h) != 2 || h[0].IP != "192.168.1.1" || len(h[1].MS) != 3 || h[1].MS[0] != -1 {
		t.Fatalf("ukrainian: %+v", h)
	}
	if h = ParseTrace([]byte(lin)); len(h) != 3 || h[0].MS[0] != 0.512 || h[2].IP != "203.0.113.9" || h[1].MS[2] != -1 {
		t.Fatalf("linux: %+v", h)
	}
	if ParseTrace([]byte("Unable to resolve target system name nosuch.")) != nil {
		t.Fatal("garbage parsed")
	}
}
