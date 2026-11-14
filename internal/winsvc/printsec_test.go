package winsvc

import "testing"

// The access list of the print server is edited as text; entries of other
// accounts must come through untouched and odd input must be refused.
func TestDaclWith(t *testing.T) {
	const sid = "S-1-5-80-1-2-3-4-5"
	const sddl = "O:BAG:BAD:(D;;CC;;;BG)(A;;CCDCSDRCWDWO;;;BA)(A;OICIIO;GA;;;BA)(A;;0x20003;;;" + sid + ")(A;CIIO;GX;;;WD)"
	add := []string{"(A;;0x20003;;;" + sid + ")", "(A;CIIO;0xf000c;;;" + sid + ")"}

	got, err := daclWith(sddl, sid, add)
	if err != nil {
		t.Fatal(err)
	}
	want := "D:(D;;CC;;;BG)(A;;CCDCSDRCWDWO;;;BA)(A;OICIIO;GA;;;BA)(A;CIIO;GX;;;WD)" + add[0] + add[1]
	if got != want {
		t.Fatalf("\nотримано: %s\nмало бути: %s", got, want)
	}
	// Granting twice gives the same list, not doubled entries.
	again, err := daclWith("O:BAG:BA"+got, sid, add)
	if err != nil || again != want {
		t.Fatalf("повторне надання змінило список: %s (%v)", again, err)
	}
	// Revoking removes only our entries; flags and the audit list are respected.
	got, err = daclWith("O:SYG:SYD:PAI(A;;FA;;;SY)(A;;0x1;;;"+sid+")S:(AU;FA;GA;;;WD)", sid, nil)
	if err != nil || got != "D:PAI(A;;FA;;;SY)" {
		t.Fatalf("відкликання: %q (%v)", got, err)
	}
	for _, bad := range []string{
		"O:BAG:BA",                      // no access list
		"O:BAG:BAD:(A;;GA;;;BA)garbage", // something after the entries
		"O:BAG:BAD:(XA;;GA;;;BA;(Member_of {SID(BA)}))", // conditional entry: nested brackets
		"O:BAG:BAD:(A;;GA;;BA)",                         // wrong number of fields
	} {
		if out, err := daclWith(bad, sid, add); err == nil {
			t.Fatalf("незвичний список прийнято: %q -> %q", bad, out)
		}
	}
}
