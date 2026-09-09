package controller

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// Supplier reconciliation API (fork §10), implementing
// "对账业务供应商渠道接口规范": the caller is a platform reconciling the billing
// it recorded for its own users against ours.

const (
	reconciliationDefaultLimit = 1000
	reconciliationMaxLimit     = 5000
	// The spec asks callers to keep a window at or under 24h; rejecting anything
	// wider keeps a single page bounded and the offset cursor cheap.
	reconciliationMaxWindowSeconds = 24 * 60 * 60
	reconciliationCurrency         = "USD"
	// Quota is an integer count of gateway units; money is derived from it, so
	// the scale below is what the spec calls "精度保留 10 位小数".
	reconciliationMoneyScale = 10
)

// GetReconciliationConsumes handles POST /api/v3/consumes.
func GetReconciliationConsumes(c *gin.Context) {
	var req dto.ConsumesRequest
	if err := common.UnmarshalBodyReusable(c, &req); err != nil {
		writeReconciliation(c, http.StatusBadRequest, dto.ReconCodeInvalidParam, "malformed request body", nil)
		return
	}
	if req.BeginTime <= 0 || req.EndTime <= 0 {
		writeReconciliation(c, http.StatusBadRequest, dto.ReconCodeInvalidParam, "beginTime and endTime are required unix seconds", nil)
		return
	}
	if req.EndTime < req.BeginTime {
		writeReconciliation(c, http.StatusBadRequest, dto.ReconCodeInvalidTimeSpan, "endTime must not be earlier than beginTime", nil)
		return
	}
	if req.EndTime-req.BeginTime > reconciliationMaxWindowSeconds {
		writeReconciliation(c, http.StatusBadRequest, dto.ReconCodeInvalidTimeSpan, "the queried window must not exceed 24 hours", nil)
		return
	}
	if req.BeginCursor < 0 {
		writeReconciliation(c, http.StatusBadRequest, dto.ReconCodeInvalidParam, "beginCursor must not be negative", nil)
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = reconciliationDefaultLimit
	}
	if limit > reconciliationMaxLimit {
		limit = reconciliationMaxLimit
	}

	// apiKeyId is the key's name: that is the identifier operators see in the
	// API-keys list and the one the caller configures on its side. Token ids are
	// internal and never surfaced in the console.
	tokenName := strings.TrimSpace(req.ApiKeyId)

	logs, total, err := model.GetReconciliationConsumeLogs(model.ReconciliationConsumeQuery{
		UserId:    c.GetInt("id"),
		BeginTime: req.BeginTime,
		EndTime:   req.EndTime,
		TokenName: tokenName,
		Offset:    req.BeginCursor,
		Limit:     limit,
	})
	if err != nil {
		common.SysError("reconciliation consumes query failed: " + err.Error())
		writeReconciliation(c, http.StatusInternalServerError, dto.ReconCodeInternal, "failed to query consume records", nil)
		return
	}

	items := make([]dto.ConsumeItem, 0, len(logs))
	for i, log := range logs {
		items = append(items, buildConsumeItem(log, req.BeginCursor+int64(i)+1))
	}
	writeReconciliation(c, http.StatusOK, dto.ReconCodeSuccess, "success", dto.ConsumesData{
		Items:       items,
		BeginTime:   req.BeginTime,
		EndTime:     req.EndTime,
		ApiKeyId:    req.ApiKeyId,
		BeginCursor: req.BeginCursor,
		Limit:       limit,
		Total:       total,
	})
}

// GetReconciliationBalance handles GET /api/v1/balance.
func GetReconciliationBalance(c *gin.Context) {
	userId := c.GetInt("id")
	quota, err := model.GetUserQuota(userId, false)
	if err != nil {
		common.SysError("reconciliation balance query failed: " + err.Error())
		writeReconciliation(c, http.StatusInternalServerError, dto.ReconCodeInternal, "failed to query balance", nil)
		return
	}
	lastUpdatedAt, err := model.GetLastBillingActivityAt(userId)
	if err != nil {
		// The balance itself is authoritative; a missing activity timestamp must
		// not fail the monitoring poll this endpoint exists for.
		common.SysError("reconciliation balance activity lookup failed: " + err.Error())
		lastUpdatedAt = 0
	}
	writeReconciliation(c, http.StatusOK, dto.ReconCodeSuccess, "success", dto.BalanceData{
		Balance:              quotaToAmount(int64(quota)),
		BalanceLastUpdatedAt: lastUpdatedAt,
		Currency:             reconciliationCurrency,
	})
}

func buildConsumeItem(log *model.Log, cursor int64) dto.ConsumeItem {
	other := map[string]any{}
	if log.Other != "" {
		if err := common.UnmarshalJsonStr(log.Other, &other); err != nil {
			other = map[string]any{}
		}
	}
	// The spec's requestId must be the caller's own correlation id when it was
	// supplied; otherwise fall back to the id this gateway returned on the AI
	// response, which the spec names as the secondary anchor.
	requestId := log.PlatformRequestId
	if requestId == "" {
		requestId = log.RequestId
	}
	return dto.ConsumeItem{
		Cursor:            cursor,
		RequestId:         requestId,
		UpstreamRequestId: log.UpstreamRequestId,
		ApiKeyId:          log.TokenName,
		Model:             log.ModelName,
		ConsumeAt:         log.CreatedAt,
		Stream:            log.IsStream,
		PriceType:         consumePriceType(other),
		UsageDetail:       buildUsageDetail(log, other),
		TotalTokens:       int64(log.PromptTokens) + int64(log.CompletionTokens),
		Quantity:          otherInt(other, "quantity"),
		TotalCost:         quotaToAmount(int64(log.Quota)),
		Currency:          reconciliationCurrency,
	}
}

// consumePriceType maps this gateway's billing modes onto the spec's vocabulary.
// Everything this deployment sells is metered per token; a fixed per-request
// price is the only other mode that can reach a consume log today.
//
// model_price is written on every text consume log, so its presence says
// nothing — only a positive value means the call was billed per request. This
// mirrors isPerCallBilling() in the log UI.
func consumePriceType(other map[string]any) string {
	if price, ok := otherFloat(other, "model_price"); ok && price > 0 {
		return "per_call"
	}
	return "token"
}

// otherFloat reads a numeric field from the decoded log metadata, which round
// trips through JSON and therefore arrives as float64.
func otherFloat(other map[string]any, key string) (float64, bool) {
	switch v := other[key].(type) {
	case float64:
		return v, true
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// usageFacts is the protocol-neutral view of a billed call, reconstructed from
// the consume log. The stored numbers carry the *upstream* provider's
// semantics, which is independent of the protocol the caller used, so both
// readings of the input count are derived once here and the per-protocol
// renderers below pick whichever one their format expects.
type usageFacts struct {
	inputExcludingCache int
	inputIncludingCache int
	output              int
	cacheRead           int
	cacheWrite          int
	cacheWrite5m        int
	cacheWrite1h        int
	textInput           int
	textOutput          int
	audioInput          int
	audioOutput         int
}

// collectUsageFacts normalizes the two input-token conventions this gateway
// stores. Anthropic reports input_tokens excluding both cache reads and cache
// writes, and the Claude adaptor keeps that convention verbatim; OpenAI reports
// prompt_tokens with cached_tokens as a subset. other.claude marks the rows
// billed under Anthropic semantics, so it decides which direction to convert.
func collectUsageFacts(log *model.Log, other map[string]any) usageFacts {
	facts := usageFacts{
		output:       log.CompletionTokens,
		cacheRead:    otherInt(other, "cache_tokens"),
		cacheWrite:   otherInt(other, "cache_creation_tokens"),
		cacheWrite5m: otherInt(other, "cache_creation_tokens_5m"),
		cacheWrite1h: otherInt(other, "cache_creation_tokens_1h"),
		textInput:    otherInt(other, "text_input"),
		textOutput:   otherInt(other, "text_output"),
		audioInput:   otherInt(other, "audio_input"),
		audioOutput:  otherInt(other, "audio_output"),
	}
	if claude, _ := other["claude"].(bool); claude {
		facts.inputExcludingCache = log.PromptTokens
		facts.inputIncludingCache = log.PromptTokens + facts.cacheRead + facts.cacheWrite
		return facts
	}
	facts.inputIncludingCache = log.PromptTokens
	facts.inputExcludingCache = max(log.PromptTokens-facts.cacheRead, 0)
	return facts
}

// buildUsageDetail renders the usage in the wire shape of the API family the
// caller actually used, keyed off the inbound request path recorded on the log.
// The gateway normalizes provider usage into its own counters instead of
// storing the vendor payload — and an upstream that is itself a new-api
// instance never returns the original structure — so this is a faithful
// re-projection of the counts, not a byte-for-byte passthrough. Fields the
// gateway does not track (reasoning tokens, service tier) are omitted rather
// than guessed.
func buildUsageDetail(log *model.Log, other map[string]any) map[string]any {
	facts := collectUsageFacts(log, other)
	path, _ := other["request_path"].(string)
	switch {
	case strings.HasPrefix(path, "/v1/messages"):
		return anthropicUsageDetail(facts)
	case strings.HasPrefix(path, "/v1/responses"):
		return openAIResponsesUsageDetail(facts)
	case strings.HasPrefix(path, "/v1beta/") && !strings.HasPrefix(path, "/v1beta/openai/"):
		return geminiUsageDetail(facts)
	default:
		// OpenAI chat/completions is both the widest family here and the shape
		// the Gemini OpenAI-compatible endpoints answer in.
		return openAIChatUsageDetail(facts)
	}
}

func anthropicUsageDetail(facts usageFacts) map[string]any {
	detail := map[string]any{
		"input_tokens":                facts.inputExcludingCache,
		"output_tokens":               facts.output,
		"cache_read_input_tokens":     facts.cacheRead,
		"cache_creation_input_tokens": facts.cacheWrite,
	}
	if facts.cacheWrite5m > 0 || facts.cacheWrite1h > 0 {
		detail["cache_creation"] = map[string]any{
			"ephemeral_5m_input_tokens": facts.cacheWrite5m,
			"ephemeral_1h_input_tokens": facts.cacheWrite1h,
		}
	}
	return detail
}

func openAIResponsesUsageDetail(facts usageFacts) map[string]any {
	return map[string]any{
		"input_tokens":  facts.inputIncludingCache,
		"output_tokens": facts.output,
		"total_tokens":  facts.inputIncludingCache + facts.output,
		"input_tokens_details": map[string]any{
			"cached_tokens":      facts.cacheRead,
			"cache_write_tokens": facts.cacheWrite,
		},
	}
}

func openAIChatUsageDetail(facts usageFacts) map[string]any {
	detail := map[string]any{
		"prompt_tokens":     facts.inputIncludingCache,
		"completion_tokens": facts.output,
		"total_tokens":      facts.inputIncludingCache + facts.output,
	}
	promptDetails := map[string]any{"cached_tokens": facts.cacheRead}
	if facts.textInput > 0 {
		promptDetails["text_tokens"] = facts.textInput
	}
	if facts.audioInput > 0 {
		promptDetails["audio_tokens"] = facts.audioInput
	}
	detail["prompt_tokens_details"] = promptDetails
	if facts.textOutput > 0 || facts.audioOutput > 0 {
		completionDetails := map[string]any{}
		if facts.textOutput > 0 {
			completionDetails["text_tokens"] = facts.textOutput
		}
		if facts.audioOutput > 0 {
			completionDetails["audio_tokens"] = facts.audioOutput
		}
		detail["completion_tokens_details"] = completionDetails
	}
	return detail
}

func geminiUsageDetail(facts usageFacts) map[string]any {
	detail := map[string]any{
		"promptTokenCount":     facts.inputIncludingCache,
		"candidatesTokenCount": facts.output,
		"totalTokenCount":      facts.inputIncludingCache + facts.output,
	}
	if facts.cacheRead > 0 {
		detail["cachedContentTokenCount"] = facts.cacheRead
	}
	if modalities := modalityBreakdown(facts.textInput, facts.audioInput); len(modalities) > 0 {
		detail["promptTokensDetails"] = modalities
	}
	if modalities := modalityBreakdown(facts.textOutput, facts.audioOutput); len(modalities) > 0 {
		detail["candidatesTokensDetails"] = modalities
	}
	return detail
}

// modalityBreakdown renders Gemini's per-modality token arrays. Only modalities
// the gateway actually counted are listed.
func modalityBreakdown(textTokens, audioTokens int) []map[string]any {
	breakdown := make([]map[string]any, 0, 2)
	if textTokens > 0 {
		breakdown = append(breakdown, map[string]any{"modality": "TEXT", "tokenCount": textTokens})
	}
	if audioTokens > 0 {
		breakdown = append(breakdown, map[string]any{"modality": "AUDIO", "tokenCount": audioTokens})
	}
	return breakdown
}

// otherInt reads a numeric field from the decoded log metadata, which round
// trips through JSON and therefore arrives as float64.
func otherInt(other map[string]any, key string) int {
	switch v := other[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	default:
		return 0
	}
}

// quotaToAmount converts internal quota units into the settlement currency.
// decimal keeps the division exact instead of leaking float error into money.
func quotaToAmount(quota int64) json.Number {
	amount := decimal.NewFromInt(quota).
		Div(decimal.NewFromFloat(common.QuotaPerUnit)).
		Round(reconciliationMoneyScale)
	return json.Number(amount.String())
}

func writeReconciliation(c *gin.Context, status int, code int, message string, data any) {
	c.JSON(status, dto.ReconciliationEnvelope{
		RequestId: c.GetString(common.RequestIdKey),
		Code:      code,
		Message:   message,
		Data:      data,
	})
}
