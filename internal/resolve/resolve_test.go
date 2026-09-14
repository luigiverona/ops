package resolve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

type missingRunner struct{}

func (missingRunner) Run(context.Context, run.Spec) (run.Result, error) {
	return run.Result{Stderr: "error: target not found: steam"}, errors.New("exit 1")
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDisabledMultilibPackageResolvesThroughOfficialAPI(t *testing.T) {
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"version":2,"valid":true,"count":1,"page":1,"num_pages":1,"results":[{"pkgname":"steam","repo":"multilib","arch":"x86_64","depends":["lib32-glibc"],"optdepends":[],"conflicts":[]}]}`
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	resolver := Resolver{Runner: missingRunner{}, Client: client}
	pkg, found, err := resolver.Pacman(context.Background(), "steam")
	if err != nil || !found || pkg.Repository != "multilib" || pkg.Name != "steam" {
		t.Fatalf("package=%#v found=%v err=%v", pkg, found, err)
	}
}

type countingRunner struct{ calls int }

func (f *countingRunner) Run(_ context.Context, _ run.Spec) (run.Result, error) {
	f.calls++
	return run.Result{}, errors.New("unexpected command")
}

func TestAURSourcePinsMetadataToExactGitCommit(t *testing.T) {
	const commit = "0123456789012345678901234567890123456789"
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/info/refs") {
			body := "001e# service=git-upload-pack\n0000" + packet(commit+" HEAD\x00object-format=sha1\n") + "0000"
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		if r.URL.Query().Get("h") != "paru" || r.URL.Query().Get("id") != commit {
			t.Fatalf("unpinned metadata request: %s", r.URL.String())
		}
		body := "pkgbase = paru\n\tpkgver = 2.1.0\n\tpkgrel = 2\n\tmakedepends = cargo\n\npkgname = paru\n"
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	runner := &countingRunner{}
	source, found, err := (Resolver{Runner: runner, Client: client}).AURSource(context.Background(), "paru")
	if runner.calls != 0 {
		t.Fatal("source discovery invoked a local command")
	}
	if err != nil || !found || source.Commit != commit || source.Metadata.PackageBase != "paru" || strings.Join(source.Metadata.MakeDepends, ",") != "cargo" {
		t.Fatalf("source=%#v found=%v err=%v", source, found, err)
	}
}

func TestAURSourceRejectsUnsafePackageBaseBeforeNetworkResolution(t *testing.T) {
	for _, name := range []string{"paru?redirect=example", ".", ".."} {
		t.Run(name, func(t *testing.T) {
			runner := &countingRunner{}
			requests := 0
			client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				requests++
				return nil, errors.New("unexpected request")
			})}
			resolver := Resolver{Runner: runner, Client: client}
			_, found, err := resolver.AURSource(context.Background(), name)
			if err == nil || found || runner.calls != 0 || requests != 0 {
				t.Fatalf("unsafe package base reached source resolution: err=%v calls=%d requests=%d", err, runner.calls, requests)
			}
			_, found, err = resolver.AUR(context.Background(), name)
			if err == nil || found || runner.calls != 0 || requests != 0 {
				t.Fatalf("unsafe package name reached RPC resolution: err=%v calls=%d requests=%d", err, runner.calls, requests)
			}
		})
	}
}

func TestAURDotPackageBasesFailPlanningBeforeCommands(t *testing.T) {
	const commit = "0123456789012345678901234567890123456789"
	for _, base := range []string{".", ".."} {
		for _, boundary := range []string{"RPC", "SRCINFO"} {
			t.Run(boundary+"/"+base, func(t *testing.T) {
				requests := 0
				client := &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
					requests++
					var body string
					switch {
					case strings.HasPrefix(req.URL.Path, "/rpc/"):
						rpcBase := "example"
						if boundary == "RPC" {
							rpcBase = base
						}
						body = fmt.Sprintf(`{"version":5,"type":"multiinfo","resultcount":1,"results":[{"Name":"example","PackageBase":%q}]}`, rpcBase)
					case req.URL.Path == "/example.git/info/refs":
						body = "001e# service=git-upload-pack\n0000" + packet(commit+" HEAD\x00object-format=sha1\n") + "0000"
					case strings.HasSuffix(req.URL.Path, "/.SRCINFO"):
						body = "pkgbase = " + base + "\npkgver = 1\npkgrel = 1\npkgname = example\n"
					default:
						t.Fatalf("unexpected metadata request: %s", req.URL)
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				runner := &countingRunner{}
				declaration := config.Application{Source: config.AUR, Identifier: "example"}
				cfg := config.Config{Version: 2, Applications: []config.Application{declaration}}
				fact := Applications(context.Background(), cfg, plan.State{}, Resolver{Runner: runner, Client: client})[declaration]
				wantRequests := 1
				wantCause := "invalid AUR package base"
				if boundary == "SRCINFO" {
					wantRequests = 3
					wantCause = ".SRCINFO contains an invalid package base"
				}
				if fact.State != plan.Unavailable || fact.Err == nil || !strings.Contains(fact.Err.Error(), wantCause) {
					t.Fatalf("invalid base was not rejected: fact=%+v", fact)
				}
				if fact.AURSource.Commit != "" || len(fact.AURPackages) != 0 || runner.calls != 0 || requests != wantRequests {
					t.Fatalf("invalid base reached build planning: fact=%+v calls=%d requests=%d", fact, runner.calls, requests)
				}
			})
		}
	}
}

type dependencyRunner struct {
	satisfied   bool
	transaction string
	calls       []run.Spec
}

func (f *dependencyRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	if spec.Name == "pacman" && (spec.Args[0] == "-Qi" || spec.Args[0] == "-Si") {
		return run.Result{Stdout: testpkg.Info(spec.Args[len(spec.Args)-1])}, nil
	}
	if len(spec.Args) > 0 && spec.Args[0] == "-T" {
		if f.satisfied {
			return run.Result{}, nil
		}
		return run.Result{Stdout: spec.Args[len(spec.Args)-1] + "\n"}, &run.Error{Name: "pacman", Err: dependencyExit(127)}
	}
	if len(spec.Args) > 0 && spec.Args[0] == "-Sp" {
		return run.Result{Stdout: f.transaction}, nil
	}
	return run.Result{}, errors.New("unexpected command")
}

func TestOfficialDependencyPreservesInstalledSatisfier(t *testing.T) {
	runner := &dependencyRunner{satisfied: true, transaction: "extra/rust\tcargo\n"}
	binding, err := (Resolver{Runner: runner}).OfficialDependency(context.Background(), "cargo")
	if err != nil {
		t.Fatal(err)
	}
	if !binding.Satisfied || binding.Provider != "extra/rust" || len(binding.Packages) != 1 || len(runner.calls) != 5 {
		t.Fatalf("binding=%#v calls=%#v", binding, runner.calls)
	}
}

func TestOfficialDependencyMaterializesPacmanProviderAndTransaction(t *testing.T) {
	tests := []string{
		"extra/rust\tcargo rustfmt\nextra/llvm-libs\t\n",
		"extra/llvm-libs\t\nextra/rust\tcargo rustfmt\n",
	}
	for _, transaction := range tests {
		runner := &dependencyRunner{transaction: transaction}
		binding, err := (Resolver{Runner: runner}).OfficialDependency(context.Background(), "cargo")
		if err != nil {
			t.Fatal(err)
		}
		if binding.Satisfied || binding.Provider != "extra/rust" || strings.Join(binding.Packages, ",") != "extra/llvm-libs,extra/rust" {
			t.Fatalf("binding=%#v", binding)
		}
		call := runner.calls[1]
		if call.Name != "pacman" || strings.Join(call.Args, " ") != "-Sp --noconfirm --print-format %r/%n\t%P -- cargo" || call.Interactive {
			t.Fatalf("non-deterministic provider query: %#v", call)
		}
	}
}

func TestParseProviderTransactionPacmanGrammar(t *testing.T) {
	valid := []struct {
		output string
		want   string
	}{
		{"extra/linux-api-headers\t\n", "extra/linux-api-headers:"},
		{"extra/rust\tcargo rustfmt\n", "extra/rust:cargo,rustfmt"},
		{"extra/provider\tvirtual=2.3\n", "extra/provider:virtual=2.3"},
		{"extra/one\t\r\nextra/two\tvirtual=1\r\n", "extra/one:,extra/two:virtual=1"},
	}
	for _, test := range valid {
		records, err := parseProviderTransaction(test.output)
		if err != nil {
			t.Fatalf("%q: %v", test.output, err)
		}
		var got []string
		for _, record := range records {
			got = append(got, record.Name+":"+strings.Join(record.Provides, ","))
		}
		if strings.Join(got, ",") != test.want {
			t.Fatalf("%q got=%q want=%q", test.output, got, test.want)
		}
	}
	for _, output := range []string{
		"missing\n", "extra/two\ttabs\there\n", "\tprovide\n", "bad/name\tprovide\n",
		"extra/pkg\tprovide>=2\n", "extra/pkg\tprovide  other\n", "extra/pkg\t\nextra/pkg\t\n",
	} {
		if _, err := parseProviderTransaction(output); err == nil {
			t.Fatalf("accepted malformed transaction %q", output)
		}
	}
}

type isolatedPacmanRunner struct{ dbpath string }

func (r isolatedPacmanRunner) Run(ctx context.Context, spec run.Spec) (run.Result, error) {
	args := append([]string(nil), spec.Args...)
	if spec.Name == "pacman" {
		args = append([]string{"--dbpath", r.dbpath}, args...)
	}
	command := exec.CommandContext(ctx, spec.Name, args...)
	output, err := command.Output()
	if err != nil {
		return run.Result{Stdout: string(output)}, err
	}
	return run.Result{Stdout: string(output)}, nil
}

func TestRealPacmanProviderPrintFormatInIsolatedDatabase(t *testing.T) {
	if _, err := exec.LookPath("pacman"); err != nil {
		t.Skip("pacman is unavailable")
	}
	if _, err := os.Stat("/var/lib/pacman/sync"); err != nil {
		t.Skip("pacman sync databases are unavailable")
	}
	dbpath := t.TempDir()
	if err := os.Symlink("/var/lib/pacman/sync", filepath.Join(dbpath, "sync")); err != nil {
		t.Fatal(err)
	}
	resolver := Resolver{Runner: isolatedPacmanRunner{dbpath: dbpath}}
	for _, requirement := range []string{"cargo", "base-devel", "java-runtime>=26"} {
		binding, err := resolver.OfficialDependency(context.Background(), requirement)
		if err != nil {
			t.Fatalf("real pacman %q: %v", requirement, err)
		}
		if binding.Satisfied || binding.Provider == "" || len(binding.Packages) == 0 {
			t.Fatalf("real pacman %q binding=%#v", requirement, binding)
		}
	}
}

func TestCompareVersionsUsesVerCmpWithoutSeparator(t *testing.T) {
	runner := &versionRunner{}
	comparison, err := (Resolver{Runner: runner}).CompareVersions(context.Background(), "1:2.0-3", "1:2.0-4")
	if err != nil || comparison != -1 || strings.Join(runner.spec.Args, " ") != "1:2.0-3 1:2.0-4" {
		t.Fatalf("comparison=%d err=%v command=%#v", comparison, err, runner.spec)
	}
}

type versionRunner struct{ spec run.Spec }

func (r *versionRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	r.spec = spec
	return run.Result{Stdout: "-1\n"}, nil
}

func TestRealVerCmpArchVersionSemantics(t *testing.T) {
	if _, err := exec.LookPath("vercmp"); err != nil {
		t.Skip("vercmp is unavailable")
	}
	resolver := Resolver{Runner: isolatedPacmanRunner{dbpath: t.TempDir()}}
	for _, test := range []struct {
		left, right string
		want        int
	}{
		{"1-1", "1-1", 0}, {"1-1", "1-2", -1}, {"2.0-1", "1.99-9", 1},
		{"1:1.0-1", "1.0-99", 1}, {"1:2.0-3", "1:2.0-4", -1},
	} {
		got, err := resolver.CompareVersions(context.Background(), test.left, test.right)
		if err != nil || got != test.want {
			t.Fatalf("vercmp %s %s = %d, %v", test.left, test.right, got, err)
		}
	}
}

func TestOfficialDependencyRejectsAmbiguousOrInvalidResolution(t *testing.T) {
	for _, transaction := range []string{
		"extra/rust\tcargo\nextra/rustup\tcargo\n",
		"extra/rust\tcargo\nextra/rust\tcargo\n",
		"not/a/package\tcargo\n",
	} {
		runner := &dependencyRunner{transaction: transaction}
		if _, err := (Resolver{Runner: runner}).OfficialDependency(context.Background(), "cargo"); err == nil {
			t.Fatalf("unsafe transaction accepted: %q", transaction)
		}
	}
}

type transactionRunner struct {
	output string
	spec   run.Spec
}

func (f *transactionRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.spec = spec
	if spec.Name != "pacman" {
		return run.Result{}, errors.New("unexpected command")
	}
	return run.Result{Stdout: f.output}, nil
}

func TestOfficialTransactionIsConcreteDeterministicAndNoninteractive(t *testing.T) {
	runner := &transactionRunner{output: "extra/rust\nextra/llvm-libs\n"}
	resolver := Resolver{Runner: runner}
	transaction, err := resolver.OfficialTransaction(context.Background(), []string{"extra/llvm-libs", "extra/rust"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(transaction, ",") != "extra/llvm-libs,extra/rust" {
		t.Fatalf("transaction=%v", transaction)
	}
	if runner.spec.Interactive || strings.Join(runner.spec.Args, " ") != "-Sp --noconfirm --print-format %r/%n -- extra/llvm-libs extra/rust" {
		t.Fatalf("query=%#v", runner.spec)
	}
}

func packet(value string) string { return fmt.Sprintf("%04x%s", len(value)+4, value) }
