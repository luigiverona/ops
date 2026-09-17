package archtrust

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// Test-only packet surgery on GnuPG exports. All verification still goes through
// production GnuPG; this helper neither authenticates nor authorizes anything.
func filterTestPackets(t *testing.T, data []byte, keep func(tag byte, body []byte) bool) []byte {
	t.Helper()
	var out []byte
	for len(data) != 0 {
		start := data
		if data[0]&0x80 == 0 {
			t.Fatal("invalid fixture packet")
		}
		tag, header, size := byte(0), 0, 0
		if data[0]&0x40 != 0 {
			tag = data[0] & 0x3f
			if len(data) < 2 {
				t.Fatal("short packet")
			}
			switch n := int(data[1]); {
			case n < 192:
				header, size = 2, n
			case n < 224:
				if len(data) < 3 {
					t.Fatal("short packet")
				}
				header, size = 3, (n-192)*256+int(data[2])+192
			case n == 255:
				if len(data) < 6 {
					t.Fatal("short packet")
				}
				header, size = 6, int(binary.BigEndian.Uint32(data[2:6]))
			default:
				t.Fatal("unexpected partial fixture packet")
			}
		} else {
			tag = (data[0] >> 2) & 15
			width := 1 << uint(data[0]&3)
			if width > 4 || len(data) < 1+width {
				t.Fatal("unsupported fixture packet")
			}
			header = 1 + width
			for _, b := range data[1:header] {
				size = size*256 + int(b)
			}
		}
		if size > len(data)-header {
			t.Fatal("short fixture body")
		}
		if keep(tag, data[header:header+size]) {
			out = append(out, start[:header+size]...)
		}
		data = data[header+size:]
	}
	return out
}

func testSignatureIssuer(body []byte, fingerprint string) bool {
	if len(body) < 6 || body[0] != 4 {
		return false
	}
	n := int(binary.BigEndian.Uint16(body[4:6]))
	if len(body) < 8+n {
		return false
	}
	m := int(binary.BigEndian.Uint16(body[6+n : 8+n]))
	if len(body) < 8+n+m {
		return false
	}
	fpr, _ := hex.DecodeString(fingerprint)
	for _, block := range [][]byte{body[6 : 6+n], body[8+n : 8+n+m]} {
		for len(block) > 0 {
			length, head := int(block[0]), 1
			if length >= 192 { // Generated issuer subpackets have short lengths.
				return false
			}
			if length < 1 || len(block) < head+length {
				return false
			}
			kind, value := block[head]&127, block[head+1:head+length]
			if kind == 33 && len(value) == 21 && string(value[1:]) == string(fpr) {
				return true
			}
			if kind == 16 && len(value) == 8 && string(value) == string(fpr[len(fpr)-8:]) {
				return true
			}
			block = block[head+length:]
		}
	}
	return false
}
