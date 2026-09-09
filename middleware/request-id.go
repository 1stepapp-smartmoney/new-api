package middleware

import (
	"context"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func RequestId() func(c *gin.Context) {
	return func(c *gin.Context) {
		id := common.NewRequestId()
		c.Set(common.RequestIdKey, id)
		ctx := context.WithValue(c.Request.Context(), common.RequestIdKey, id)
		c.Request = c.Request.WithContext(ctx)
		c.Header(common.RequestIdKey, id)
		if platformId := sanitizePlatformRequestId(c.Request.Header.Get(common.PlatformRequestIdHeader)); platformId != "" {
			c.Set(common.PlatformRequestIdKey, platformId)
		}
		c.Next()
	}
}

// sanitizePlatformRequestId normalizes the caller-supplied reconciliation
// correlation id (fork §10) before it is stored on the consume log and echoed
// back by the reconciliation API. The value is caller-controlled, so it is
// trimmed to the column width and stripped of control characters that would
// corrupt logs or JSON responses. An empty result disables capture for the
// request, leaving the gateway's own request id as the reconciliation anchor.
func sanitizePlatformRequestId(raw string) string {
	if common.PlatformRequestIdHeader == "" || raw == "" {
		return ""
	}
	cleaned := strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(raw))
	if runes := []rune(cleaned); len(runes) > common.MaxPlatformRequestIdLength {
		cleaned = string(runes[:common.MaxPlatformRequestIdLength])
	}
	return strings.TrimSpace(cleaned)
}
