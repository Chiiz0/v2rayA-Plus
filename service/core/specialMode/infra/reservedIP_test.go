package infra

import "testing"

func TestReservedIP(t *testing.T) {
	var tests = [][2]interface{}{
		{reservedIP(155), [4]byte{198, 18, 0, 155}},
		{reservedIP(257), [4]byte{198, 18, 1, 1}},
		{reservedIP(256*3 + 254).Next(), [4]byte{198, 18, 4, 1}},
	}
	for _, test := range tests {
		r := test[0].(reservedIP)
		if a, b := r.IP(), test[1].([4]byte); a != b {
			t.Fatal("expect", b, "but got", a)
		}
	}
}
