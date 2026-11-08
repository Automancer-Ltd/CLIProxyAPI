package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type websocketHubAuthority struct{ lost atomic.Bool }

func (a *websocketHubAuthority) Authorize(context.Context, *coreauth.Auth) error {
	if a.lost.Load() {
		return errors.New("fixture lost ownership")
	}
	return nil
}
func (a *websocketHubAuthority) Refresh(context.Context, *coreauth.Auth, func(context.Context, *coreauth.Auth) (*coreauth.Auth, error)) (*coreauth.Auth, error) {
	return nil, errors.New("not used")
}
func (a *websocketHubAuthority) OpenHubSession(context.Context, *coreauth.Auth) (func(context.Context) error, error) {
	return func(ctx context.Context) error { return a.Authorize(ctx, nil) }, nil
}

// A decoded frame may already sit in Gorilla's buffer or the duplex read queue.
// Both must be reauthorized at delivery, even without another network read.
func TestCodexHubRejectsBufferedFrames(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "socket-buffer", true: "duplex-queue"}[queued], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				// Both complete uncompressed frames go in the same network write.
				_, _ = conn.UnderlyingConn().Write([]byte{0x81, 5, 'f', 'i', 'r', 's', 't', 0x81, 6, 's', 'e', 'c', 'o', 'n', 'd'})
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			authority := &websocketHubAuthority{}
			a := &coreauth.Auth{ID: "fixture", Provider: "codex", HubAuthority: authority}
			e := NewCodexWebsocketsExecutor(&config.Config{})
			conn, closer, _, err := e.dialCodexWebsocket(context.Background(), a, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer closer.Close()
			_, first, err := readCodexWebsocketMessage(context.Background(), nil, conn, nil)
			if err != nil || string(first) != "first" {
				t.Fatal("initial frame failed", err)
			}
			authority.lost.Store(true)
			var sess *codexWebsocketSession
			var ch chan codexWebsocketRead
			if queued {
				sess = &codexWebsocketSession{}
				ch = make(chan codexWebsocketRead, 1)
				ch <- codexWebsocketRead{conn: conn, msgType: websocket.TextMessage, payload: []byte("queued")}
			}
			if _, raw, err := readCodexWebsocketMessage(context.Background(), sess, conn, ch); err == nil || len(raw) != 0 {
				t.Fatal("revoked buffered frame delivered")
			}
			authority.lost.Store(false)
			if err := conn.WriteMessage(websocket.TextMessage, []byte("resurrect")); err == nil {
				t.Fatal("revoked socket resurrected")
			}
		})
	}
}
