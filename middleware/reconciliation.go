package middleware

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// ReconciliationAuth authenticates the supplier reconciliation endpoints
// (fork §10) with the spec's `x-api-key` header. It deliberately does not reuse
// TokenAuth: those endpoints must not inherit the relay context (model limits,
// channel selection, quota pre-consume), and the spec requires failures to be
// reported through the reconciliation envelope rather than an OpenAI-style
// error body.
//
// The key itself stays an ordinary token, so operators grant it "reconciliation
// only" access with the existing controls: enable the model allow-list and
// leave it empty, which makes every relay call fail with 403, and set unlimited
// quota so the key is never treated as exhausted. These endpoints read data and
// never consume quota.
func ReconciliationAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		key := strings.TrimSpace(c.Request.Header.Get("x-api-key"))
		key = strings.TrimPrefix(key, "sk-")
		if key == "" {
			abortReconciliation(c, http.StatusUnauthorized, dto.ReconCodeUnauthorized, "missing x-api-key header")
			return
		}
		// Tokens may carry a `-channelId` suffix for admin channel pinning; that
		// is a relay-only feature, so only the key part is meaningful here.
		key, _, _ = strings.Cut(key, "-")

		token, err := model.ValidateUserToken(key)
		if err != nil || token == nil {
			abortReconciliation(c, http.StatusUnauthorized, dto.ReconCodeUnauthorized, "invalid or expired x-api-key")
			return
		}
		// Only a key that can call no model at all may read the billing ledger.
		// "Model allow-list enabled, list empty" is precisely the configuration
		// that makes a key reconciliation-only, so it doubles as the marker:
		// an ordinary relay key is rejected here, and a leaked one therefore
		// cannot enumerate the account's consumption history or balance.
		if !token.ModelLimitsEnabled || len(token.GetModelLimitsMap()) > 0 {
			abortReconciliation(c, http.StatusForbidden, dto.ReconCodeForbidden,
				"this key is not a reconciliation key: enable the model allow-list and leave it empty")
			return
		}

		userCache, err := model.GetUserCache(token.UserId)
		if err != nil || userCache.Status != common.UserStatusEnabled {
			abortReconciliation(c, http.StatusForbidden, dto.ReconCodeForbidden, "the account bound to this key is unavailable")
			return
		}

		c.Set("id", token.UserId)
		c.Set("token_id", token.Id)
		c.Next()
	}
}

func abortReconciliation(c *gin.Context, status int, code int, message string) {
	c.JSON(status, dto.ReconciliationEnvelope{
		RequestId: c.GetString(common.RequestIdKey),
		Code:      code,
		Message:   message,
	})
	c.Abort()
}
