// Package hubauth coordinates the estate hubs through Tracker's metadata authority.
// Provider credentials belong only in encrypted objects, never in metadata calls.
package hubauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Fence struct {
	Host       string `json:"host"`
	InstanceID string `json:"instance_id"`
	Epoch      int64  `json:"epoch"`
}

type Pointer struct {
	ObjectKey string `json:"object_key"`
	SHA256    string `json:"ciphertext_sha256"`
}

type Head struct {
	Revision int64    `json:"revision"`
	Snapshot *Pointer `json:"snapshot"`
}

type Ownership struct {
	Host             string `json:"host"`
	InstanceID       string `json:"instance_id"`
	Epoch            int64  `json:"epoch"`
	Live             bool   `json:"live"`
	ExpiresInMS      int64  `json:"expires_in_ms"`
	Phase            string `json:"phase"`
	HydratedRevision *int64 `json:"hydrated_revision"`
	CanServe         bool   `json:"can_serve"`
}

type State struct {
	Head      Head      `json:"head"`
	Ownership Ownership `json:"ownership"`
	Transfer  *struct {
		To string `json:"to"`
	} `json:"transfer"`
}

type Account struct {
	ID          string `json:"account_id"`
	Generation  int64  `json:"generation"`
	Status      string `json:"status"`
	Revision    int64  `json:"revision"`
	Quarantined bool   `json:"quarantined"`
}

type Intent struct {
	ID               string  `json:"intent_id"`
	AccountID        string  `json:"account_id"`
	Generation       int64   `json:"generation"`
	ExpectedRevision int64   `json:"expected_revision"`
	Host             string  `json:"host"`
	InstanceID       string  `json:"instance_id"`
	Epoch            int64   `json:"epoch"`
	ResultKey        string  `json:"result_object_key"`
	State            string  `json:"state"`
	ResultSHA256     *string `json:"result_sha256"`
}

type Change struct {
	Kind               string   `json:"kind"`
	AccountID          string   `json:"account_id"`
	ExpectedGeneration int64    `json:"expected_generation"`
	IntentID           string   `json:"intent_id,omitempty"`
	ResultSHA256       string   `json:"result_sha256,omitempty"`
	Snapshot           *Pointer `json:"snapshot,omitempty"`
}

type Commit struct {
	Authority        Fence  `json:"authority"`
	ExpectedRevision int64  `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
	Change           Change `json:"change"`
}

type Receipt struct {
	OperationID string  `json:"operation_id"`
	Head        Head    `json:"head"`
	Account     Account `json:"account"`
	Kind        string  `json:"kind"`
	IntentID    *string `json:"intent_id"`
}

// Refusal is a confirmed authority rejection. Other errors are ambiguous.
// Neither provider bodies nor credential-bearing transport errors are exposed.
type Refusal struct {
	Code   string
	Status int
}

func (e *Refusal) Error() string {
	return fmt.Sprintf("hub authority refused request (%d %s)", e.Status, e.Code)
}
func IsRefusal(err error, code string) bool {
	var refusal *Refusal
	return errors.As(err, &refusal) && refusal.Code == code
}

type Client struct {
	endpoint string
	id       string
	secret   string
	http     *http.Client
}

// NewClient reads only this host's protected Access files. Loopback HTTP is
// permitted for synthetic fixtures; other endpoints require HTTPS.
func NewClient(endpoint, idFile, secretFile string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return nil, errors.New("hub authority requires HTTPS or a loopback fixture")
	}
	id, err := readSecret(idFile)
	if err != nil {
		return nil, err
	}
	secret, err := readSecret(secretFile)
	if err != nil {
		return nil, err
	}
	return &Client{endpoint: endpoint, id: id, secret: secret, http: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func readSecret(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("hub secret file missing or not a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 8192 || len(bytes.TrimSpace(data)) == 0 {
		return "", errors.New("hub secret file unreadable or invalid")
	}
	return strings.TrimSpace(string(data)), nil
}

func (c *Client) call(ctx context.Context, tool string, input, output any) error {
	body, err := json.Marshal(struct {
		Tool  string `json:"tool"`
		Input any    `json:"input"`
	}{"tracker.hub." + tool, input})
	if err != nil {
		return errors.New("hub authority request encoding failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("hub authority request invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("CF-Access-Client-Id", c.id)
	req.Header.Set("CF-Access-Client-Secret", c.secret)
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("hub authority transport failed; outcome unknown")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return errors.New("hub authority reply unreadable; outcome unknown")
	}
	var envelope struct {
		OK    bool            `json:"ok"`
		Value json.RawMessage `json:"value"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return &Refusal{Status: resp.StatusCode}
	}
	if json.Unmarshal(data, &envelope) != nil {
		return errors.New("hub authority reply invalid; outcome unknown")
	}
	if resp.StatusCode == 422 && !envelope.OK {
		code := envelope.Error.Code
		// The server's explanatory text may contain supplied data; never return it.
		if !strings.HasPrefix(code, "HUB_") || len(code) > 64 {
			code = ""
		}
		return &Refusal{Code: code, Status: 422}
	}
	if resp.StatusCode != 200 || !envelope.OK || len(envelope.Value) == 0 {
		return errors.New("hub authority call failed; outcome unknown")
	}
	if json.Unmarshal(envelope.Value, output) != nil {
		return errors.New("hub authority value invalid; outcome unknown")
	}
	return nil
}
