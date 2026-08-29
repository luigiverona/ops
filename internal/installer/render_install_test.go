package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderInstallScriptSyntax(t *testing.T) {
	path, err := filepath.Abs("../../script/render-install.sh")
	if err != nil {
		t.Fatal(err)
	}

	if output, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v: %s", err, output)
	}
}

func TestRenderInstallerProvisionedTrust(t *testing.T) {
	script, err := filepath.Abs("../../script/render-install.sh")
	if err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(t.TempDir(), "install")
	cmd := exec.Command("sh", script, outputPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("render installer: %v: %s", err, output)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(data)

	const fingerprint = "EB564BFFD8F63A984BF72A0237A80EDB682BBBFD"

	if strings.Contains(rendered, "@OPS_SIGNING_") {
		t.Fatal("rendered installer contains unresolved signing placeholders")
	}
	if strings.Count(rendered, fingerprint) != 1 {
		t.Fatalf("rendered installer contains signing fingerprint %d times", strings.Count(rendered, fingerprint))
	}
	if !strings.Contains(rendered, "-----BEGIN PGP PUBLIC KEY BLOCK-----") ||
		!strings.Contains(rendered, "-----END PGP PUBLIC KEY BLOCK-----") {
		t.Fatal("rendered installer does not contain the signing public key")
	}
}
