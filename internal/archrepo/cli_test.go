package archrepo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

// These probes use only synthetic local/sync databases and print/query modes.
// They never refresh databases, download packages, acquire sudo or install.
func TestRealPacmanSourceContracts(t *testing.T) {
	if _, err := exec.LookPath("pacman"); err != nil {
		t.Skip("pacman unavailable")
	}
	dir := t.TempDir()
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	desc := func(name, packager, extra string) string {
		return "%NAME%\n" + name + "\n\n%VERSION%\n1-1\n\n%BASE%\n" + name + "\n\n%FILENAME%\n" + name + "-1-1-any.pkg.tar.zst\n\n%DESC%\nfixture\n\n%ARCH%\nany\n\n%BUILDDATE%\n1700000000\n\n%PACKAGER%\n" + packager + "\n\n%ISIZE%\n1\n\n%CSIZE%\n1\n\n" + extra
	}
	for _, repo := range []string{"custom", "core", "extra", "multilib"} {
		var data bytes.Buffer
		gz := gzip.NewWriter(&data)
		tw := tar.NewWriter(gz)
		packages := map[string]string{}
		if repo == "custom" || repo == "extra" {
			packages["ops-probe"] = desc("ops-probe", repo+" builder", "%DEPENDS%\nops-lib\n\n%PROVIDES%\nops-virtual\n\n")
			packages["ops-lib"] = desc("ops-lib", repo+" builder", "")
		}
		if repo == "multilib" {
			packages["ops-multi"] = desc("ops-multi", "official builder", "")
		}
		for name, text := range packages {
			if err := tw.WriteHeader(&tar.Header{Name: name + "-1-1/desc", Mode: 0600, Size: int64(len(text))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(text)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(dir, "db/sync", repo+".db"), data.Bytes())
	}
	write(filepath.Join(dir, "db/local/ALPM_DB_VERSION"), []byte("9\n"))
	configuration := "[options]\nArchitecture = x86_64\nSigLevel = Never\n[custom]\nServer = file:///unused\n[core]\nServer = file:///unused\n[extra]\nServer = file:///unused\n[multilib]\nServer = file:///unused\n"
	configPath := filepath.Join(dir, "pacman.conf")
	write(configPath, []byte(configuration))
	query := func(args ...string) run.Result {
		t.Helper()
		args = append([]string{"--config", configPath, "--dbpath", filepath.Join(dir, "db"), "--root", dir, "--logfile", filepath.Join(dir, "pacman.log")}, args...)
		result, err := (run.Exec{}).Run(context.Background(), run.Spec{Name: "pacman", Args: args})
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, result.Stderr)
		}
		return result
	}
	// Qualification selects the requested repository, but does not constrain
	// implicit dependencies. The parser must reject the custom transaction member.
	transaction := query("-Sp", "--noconfirm", "--print-format", "%r/%n", "--", "extra/ops-probe").Stdout
	if !strings.Contains(transaction, "extra/ops-probe\n") || !strings.Contains(transaction, "custom/ops-lib\n") {
		t.Fatal(transaction)
	}
	if _, err := ParseTransaction(transaction); err == nil {
		t.Fatal("custom dependency accepted")
	}
	filtered, _, err := OfficialConfig(configuration)
	if err != nil {
		t.Fatal(err)
	}
	write(configPath, []byte(filtered))
	transaction = query("-Sp", "--noconfirm", "--print-format", "%r/%n", "--", "extra/ops-probe").Stdout
	if got, err := ParseTransaction(transaction); err != nil || strings.Join(got, ",") != "extra/ops-lib,extra/ops-probe" {
		t.Fatalf("%q %v", transaction, err)
	}
	provider := query("-Sp", "--noconfirm", "--print-format", "%r/%n\t%P", "--", "ops-virtual").Stdout
	if !strings.Contains(provider, "extra/ops-probe\tops-virtual\n") || !strings.Contains(provider, "extra/ops-lib\t\n") {
		t.Fatal(provider)
	}
	if got := query("-Sp", "--print-format", "%r/%n", "--", "multilib/ops-multi").Stdout; got != "multilib/ops-multi\n" {
		t.Fatal(got)
	}
	// An equal-version custom local package is native, and --needed skips its
	// qualified official counterpart despite different build metadata.
	for _, name := range []string{"ops-probe", "ops-lib"} {
		write(filepath.Join(dir, "db/local", name+"-1-1/desc"), []byte(desc(name, "custom builder", "%REASON%\n0\n\n")))
		write(filepath.Join(dir, "db/local", name+"-1-1/files"), []byte("%FILES%\n\n"))
	}
	write(configPath, []byte(configuration))
	if got := query("-Qnq", "--", "ops-probe").Stdout; got != "ops-probe\n" {
		t.Fatal(got)
	}
	if got := query("-Sp", "--needed", "--noconfirm", "--print-format", "%r/%n", "--", "extra/ops-probe").Stdout; got != "" {
		t.Fatal(got)
	}
	if got := query("-Sp", "--noconfirm", "--print-format", "%r/%n", "--", "extra/ops-probe").Stdout; got != "extra/ops-probe\n" {
		t.Fatal(got)
	}
	local, err := ParseInfo(query("-Qi", "--", "ops-probe").Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if local["Repository"] != "" || local["Packager"] != "custom builder" {
		t.Fatal(local)
	}
}
