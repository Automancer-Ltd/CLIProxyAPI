package usage

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
)

type laneContextKey struct{}

// CaptureLane snapshots the inbound request's LaneHeader into ctx while the
// HTTP handler is active, so a record published after the handler returns
// never reads a reused gin.Context. An existing snapshot is kept.
func CaptureLane(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	if _, ok := ctx.Value(laneContextKey{}).(string); ok {
		return ctx
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil {
		return ctx
	}
	return context.WithValue(ctx, laneContextKey{}, strings.TrimSpace(ginCtx.Request.Header.Get(LaneHeader)))
}

// LaneFromContext returns the lane captured by CaptureLane, or "".
func LaneFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	lane, _ := ctx.Value(laneContextKey{}).(string)
	return lane
}
