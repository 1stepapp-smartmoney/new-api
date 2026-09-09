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
// A reconciliation key is an ordinary token created with **no quota**: the relay
// path rejects a token whose remaining quota is exhausted, so such a key can
// never call a model, and these endpoints read data without consuming quota.
// That same shape is the marker, which is why the middleware does its own
// validation instead of calling model.ValidateUserToken — that helper treats a
// zero-quota token as invalid, which is exactly the token we want to admit.
//
// `used_quota == 0` distinguishes "created without quota" from "ordinary relay
// key that ran out": a key that ever billed anything has a non-zero used quota,
// so exhausting a normal key never silently grants access to the ledger.
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

		token, err := model.GetTokenByKey(key, false)
		if err != nil || token == nil {
			abortReconciliation(c, http.StatusUnauthorized, dto.ReconCodeUnauthorized, "invalid x-api-key")
			return
		}
		// A zero-quota token may legitimately carry the exhausted status, so both
		// it and enabled are admitted here; disabled and expired are not.
		if token.Status != common.TokenStatusEnabled && token.Status != common.TokenStatusExhausted {
			abortReconciliation(c, http.StatusUnauthorized, dto.ReconCodeUnauthorized, "this key is disabled or expired")
			return
		}
		if token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp() {
			abortReconciliation(c, http.StatusUnauthorized, dto.ReconCodeUnauthorized, "this key has expired")
			return
		}
		if token.UnlimitedQuota || token.RemainQuota > 0 || token.UsedQuota != 0 {
			abortReconciliation(c, http.StatusForbidden, dto.ReconCodeForbidden,
				"this key is not a reconciliation key: create a dedicated key with zero quota")
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
