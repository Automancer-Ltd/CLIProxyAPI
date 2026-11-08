package claude

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type hubFixtureTransport func(*http.Request) (*http.Response, error)

func (f hubFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSharedRefreshOncePreservesRotationBeforeProfileAndRefusesRedirect(t *testing.T) {
	for _, status := range []int{200, 307, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			svc := &ClaudeAuth{httpClient: &http.Client{Transport: hubFixtureTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "POST" {
					t.Fatal("profile read delayed durable rotation")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://fixture.invalid/redirect"}}, Body: io.NopCloser(strings.NewReader(`{"access_token":"fixture-access","refresh_token":"fixture-rotated","expires_in":3600}`)), Request: req}, nil
			})}}
			data, err := svc.RefreshTokensOnce(context.Background(), "fixture-original")
			if calls != 1 {
				t.Fatal("rotation replayed")
			}
			if status == 200 && (err != nil || data.RefreshToken != "fixture-rotated") {
				t.Fatal("rotation lost", err)
			}
			if status != 200 && err == nil {
				t.Fatal("failed rotation accepted")
			}
		})
	}
}
