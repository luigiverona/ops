package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luigiverona/ops/internal/run"
)

type metadataReadFailure struct{ err error }

func (r metadataReadFailure) Read([]byte) (int, error) { return 0, r.err }

func TestMetadataFetchRetainsFatalCauses(t *testing.T) {
	owner := &run.OwnershipError{Err: errors.New("original metadata ownership failure")}
	for name, cause := range map[string]error{
		"ownership":                      owner,
		"wrapped ownership":              fmt.Errorf("transport: %w", owner),
		"canceled":                       context.Canceled,
		"deadline":                       context.DeadlineExceeded,
		"unavailable wrapping ownership": metadataUnavailableError{err: owner},
	} {
		for _, phase := range []string{"request", "body"} {
			t.Run(name+"/"+phase, func(t *testing.T) {
				m := managerWithMetadata(t, t.TempDir())
				if err := m.ConfigureGitHub(context.Background()); err != nil {
					t.Fatal(err)
				}
				requests, later := 0, 0
				m.HTTP = &http.Client{Transport: metadataTransport(func(*http.Request) (*http.Response, error) {
					requests++
					if phase == "request" {
						return nil, cause
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(metadataReadFailure{cause}), Header: make(http.Header)}, nil
				})}
				base := m.Runner
				m.Runner = sshRunnerFunc(func(ctx context.Context, s run.Spec) (run.Result, error) {
					if requests != 0 {
						later++
					}
					return base.Run(ctx, s)
				})
				keys, err := m.fetchGitHubHostKeys(context.Background())
				if !errors.Is(err, cause) || keys != nil {
					t.Fatalf("keys=%v err=%v", keys, err)
				}
				requests = 0
				status, err := m.InspectGitHubConfiguration(context.Background())
				if !errors.Is(err, cause) || status != (GitHubConfigurationStatus{}) || requests != 1 || later != 0 {
					t.Fatalf("status=%+v err=%v requests=%d later=%d", status, err, requests, later)
				}
				if run.OwnershipFailed(cause) {
					var got *run.OwnershipError
					if !errors.As(err, &got) || got != owner {
						t.Fatalf("ownership identity lost: %v", err)
					}
				}
			})
		}
	}
}

func TestMetadataFetchCallerInterruptionRetainsTransportCause(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := context.Canceled
		if deadline {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		} else {
			cancel()
		}
		owner := &run.OwnershipError{Err: errors.New("simultaneous ownership failure")}
		for _, cause := range []error{owner, errors.New("transport stopped")} {
			m := Manager{HTTP: &http.Client{Transport: metadataTransport(func(*http.Request) (*http.Response, error) { return nil, cause })}}
			_, err := m.fetchGitHubHostKeys(ctx)
			if !errors.Is(err, want) || !errors.Is(err, cause) || metadataUnavailable(err) {
				t.Fatalf("lost causes: %v", err)
			}
		}
		cancel()
	}
}

func TestMetadataRealHTTPClientTimeout(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			m := managerWithMetadata(t, t.TempDir())
			if err := m.ConfigureGitHub(context.Background()); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if phase == "body" {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
				}
			}))
			defer server.Close()
			m.HTTP, m.MetadataURL = server.Client(), server.URL
			m.HTTP.Timeout = 50 * time.Millisecond
			ctx := context.Background()
			status, err := m.InspectGitHubConfiguration(ctx)
			var timeout net.Error
			if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &timeout) || !timeout.Timeout() || ctx.Err() != nil || status != (GitHubConfigurationStatus{}) {
				t.Fatalf("status=%+v err=%v parent=%v", status, err, ctx.Err())
			}
		})
	}
}

func TestGitHubConfiguredRetainsLocalInspectionFailure(t *testing.T) {
	m := managerWithMetadata(t, t.TempDir())
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{&run.OwnershipError{Err: errors.New("local inspection")}, context.Canceled, context.DeadlineExceeded} {
		m.Runner = sshRunnerFunc(func(context.Context, run.Spec) (run.Result, error) { return run.Result{}, cause })
		if ready, err := m.GitHubConfigured(context.Background()); ready || !errors.Is(err, cause) {
			t.Fatalf("ready=%v err=%v", ready, err)
		}
	}
}

func TestMetadataOrdinarySourceUnavailability(t *testing.T) {
	m := managerWithMetadata(t, t.TempDir())
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, cause := range map[string]error{
		"DNS unavailable":                        &net.DNSError{Err: "no such host", Name: "metadata.invalid", IsNotFound: true},
		"DNS timeout without operation deadline": &net.DNSError{Err: "DNS unavailable", Name: "metadata.invalid", IsTimeout: true},
		"network unavailable":                    errors.New("network unreachable"),
		"TLS failure":                            errors.New("TLS handshake failed"),
		"body truncated":                         io.ErrUnexpectedEOF,
	} {
		t.Run(name, func(t *testing.T) {
			m.HTTP = &http.Client{Transport: metadataTransport(func(*http.Request) (*http.Response, error) {
				if name == "body truncated" {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(metadataReadFailure{cause}), Header: make(http.Header)}, nil
				}
				return nil, cause
			})}
			status, err := m.InspectGitHubConfiguration(context.Background())
			if err != nil || !status.LocalReady || status.Freshness != HostKeyFreshnessUnavailable {
				t.Fatalf("status=%+v err=%v", status, err)
			}
		})
	}
	for _, code := range []int{http.StatusRequestTimeout, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			m.HTTP = &http.Client{Transport: metadataTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(http.NoBody), Header: make(http.Header)}, nil
			})}
			status, err := m.InspectGitHubConfiguration(context.Background())
			if err != nil || !status.LocalReady || status.Freshness != HostKeyFreshnessUnavailable {
				t.Fatalf("status=%+v err=%v", status, err)
			}
		})
	}
}
