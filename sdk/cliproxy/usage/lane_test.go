package usage

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type recordChannel chan Record

func (c recordChannel) HandleUsage(_ context.Context, record Record) { c <- record }

// A lane's spend is attributed only if the inbound X-CPA-Lane header reaches the record.
func TestPublishCarriesLaneHeaderIntoRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	ginCtx.Request.Header.Set(LaneHeader, " ln-1a2b3c4d ")

	m := NewManager(1)
	got := make(recordChannel, 1)
	m.Register(got)
	defer m.Stop()
	// The handler captures while the request is live; the gin.Context may be reused before the record is published.
	ctx := CaptureIdentity(context.WithValue(context.Background(), "gin", ginCtx))
	ginCtx.Request.Header.Set(LaneHeader, "ln-reused00")
	m.Publish(ctx, Record{Provider: "claude", Model: "claude-opus-4-5"})

	select {
	case record := <-got:
		if record.Lane != "ln-1a2b3c4d" {
			t.Fatalf("record.Lane = %q, want ln-1a2b3c4d", record.Lane)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("usage record was not delivered")
	}
}
