package archtrust

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/luigiverona/ops/internal/run"
)

// Prepared contains authenticated source bytes for a protected transaction.
// Only the production Source or explicit test fixtures supply this capability.
type Prepared struct {
	Databases map[string][]byte
	Archives  []Archive
}

type Archive struct {
	Target, Filename string
	Digest           [32]byte
	Signature        []byte
	File             *os.File
}

func (p *Prepared) Close() {
	for _, a := range p.Archives {
		name := a.File.Name()
		a.File.Close()
		os.Remove(name)
	}
}

// Prepare is called only after top-level approval. Archives are authenticated
// before being handed to privileged staging. Doctor never calls this method.
func (s *Source) Prepare(ctx context.Context, runner run.Runner, targets []string) (_ *Prepared, returnErr error) {
	s.mu.Lock()
	err := s.load(ctx)
	database := s.database
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// Avoid turning independent metadata acquisition into a partial upgrade.
	// The user's full -Syu may have used a lagging mirror; every pending official
	// upgrade must be included in this same corrective transaction.
	pending, err := s.Query(ctx, runner, []string{"-Sup", "--noconfirm", "--print-format", "%r/%n"})
	if err != nil {
		return nil, err
	}
	approved := map[string]bool{}
	for _, target := range targets {
		approved[target] = true
	}
	for _, target := range strings.Split(strings.TrimSpace(pending.Stdout), "\n") {
		if target != "" && !approved[target] {
			return nil, fmt.Errorf("configured full upgrade is not current with the independent official source (%s remains); reconcile the system upgrade and rerun ops", target)
		}
	}
	prepared := &Prepared{Databases: database}
	defer func() {
		if returnErr != nil {
			prepared.Close()
		}
	}()
	keys, err := systemKeys()
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		p, found, err := s.Lookup(ctx, target)
		if err != nil || !found {
			return nil, fmt.Errorf("official transaction target unavailable: %s: %v", target, err)
		}
		file, err := s.download(ctx, p)
		if err != nil {
			return nil, err
		}
		prepared.Archives = append(prepared.Archives, Archive{Target: target, Filename: p.filename, Digest: p.digest, Signature: p.signature, File: file})
		if _, err := authenticateArchive(ctx, runner, p, file, keys); err != nil {
			return nil, err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	after, err := systemKeys()
	if err != nil || !sameKeys(keys, after) {
		return nil, fmt.Errorf("official keyring changed during transaction preparation")
	}
	return prepared, nil
}

// download retrieves exactly one snapshot-bound archive into unprivileged
// temporary storage. It never writes pacman's cache or changes package state.
// The caller must authenticate the bytes and close/remove the returned file.
func (s *Source) download(ctx context.Context, p Package) (_ *os.File, returnErr error) {
	file, err := os.CreateTemp("", "ops-official-archive-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			file.Close()
			os.Remove(file.Name())
		}
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, Endpoint+"/"+p.repository+"/os/x86_64/"+p.filename, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("official archive evidence unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || (response.ContentLength >= 0 && response.ContentLength != p.size) {
		return nil, fmt.Errorf("official archive evidence changed or unavailable (HTTP %d)", response.StatusCode)
	}
	n, err := io.Copy(file, io.LimitReader(response.Body, p.size+1))
	if err != nil || n != p.size {
		return nil, fmt.Errorf("incomplete official archive evidence: %v", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return file, nil
}
