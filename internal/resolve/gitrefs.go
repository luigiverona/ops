package resolve

import (
	"errors"
	"strconv"
	"strings"
)

// advertisedHEAD reads only smart HTTP v0 reference discovery, not Git objects.
// HTTPS authenticates the AUR endpoint just as for git ls-remote. Apply fetches
// this exact object ID and compares the reviewed .SRCINFO before building.
// Protocol: https://git-scm.com/docs/http-protocol
func advertisedHEAD(data []byte) (string, error) {
	const prefix = "001e# service=git-upload-pack\n0000"
	if !strings.HasPrefix(string(data), prefix) {
		return "", errors.New("invalid AUR Git service advertisement")
	}
	data = data[len(prefix):]
	head := ""
	refs := map[string]bool{}
	first := true
	for len(data) >= 4 {
		size, err := strconv.ParseUint(string(data[:4]), 16, 16)
		if err != nil {
			return "", errors.New("invalid Git packet length")
		}
		if size == 0 {
			if len(data) != 4 || head == "" {
				return "", errors.New("AUR HEAD advertisement is incomplete or has trailing data")
			}
			return head, nil
		}
		if size <= 4 || size > 65520 || int(size) > len(data) {
			return "", errors.New("truncated or invalid Git packet")
		}
		line := strings.TrimSuffix(string(data[4:size]), "\n")
		data = data[size:]
		ref, capabilities, hasCapabilities := strings.Cut(line, "\x00")
		if first != hasCapabilities || strings.ContainsRune(capabilities, '\x00') {
			return "", errors.New("invalid Git capability placement")
		}
		first = false
		fields := strings.Split(ref, " ")
		if len(fields) != 2 || !gitObject.MatchString(fields[0]) || strings.Trim(fields[0], "0") == "" || fields[1] == "" || strings.ContainsAny(fields[1], "\t\r\n\x00") || refs[fields[1]] {
			return "", errors.New("invalid or duplicate AUR Git reference")
		}
		refs[fields[1]] = true
		if fields[1] == "HEAD" {
			head = strings.ToLower(fields[0])
		}
	}
	return "", errors.New("AUR Git advertisement is missing its final flush")
}
