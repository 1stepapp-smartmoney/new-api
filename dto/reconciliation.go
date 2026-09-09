package dto

import "encoding/json"

// Reconciliation DTOs implement the supplier side of the "对账业务供应商渠道接口规范"
// (fork §10): the caller is the platform reconciling its own billing against
// ours, so field names follow that spec's camelCase wire format rather than the
// snake_case used by the rest of this gateway's API.

// ReconciliationEnvelope is the shared response wrapper. code == 0 means success.
type ReconciliationEnvelope struct {
	RequestId string `json:"requestId"`
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data,omitempty"`
}

// Reconciliation response codes defined by the spec.
const (
	ReconCodeSuccess         = 0
	ReconCodeUnauthorized    = 1001
	ReconCodeForbidden       = 1002
	ReconCodeInvalidParam    = 2001
	ReconCodeInvalidTimeSpan = 2002
	ReconCodeInternal        = 9999
)

// ConsumesRequest is the POST /api/v3/consumes body.
type ConsumesRequest struct {
	BeginTime   int64  `json:"beginTime"`
	EndTime     int64  `json:"endTime"`
	ApiKeyId    string `json:"apiKeyId"`
	BeginCursor int64  `json:"beginCursor"`
	Limit       int    `json:"limit"`
}

// ConsumeItem is one billed model call.
type ConsumeItem struct {
	Cursor            int64  `json:"cursor"`
	RequestId         string `json:"requestId"`
	UpstreamRequestId string `json:"upstreamRequestId,omitempty"`
	ApiKeyId          string `json:"apiKeyId"`
	Model             string `json:"model"`
	ConsumeAt         int64  `json:"consumeAt"`
	Stream            bool   `json:"stream"`
	PriceType         string `json:"priceType"`
	UsageDetail       any    `json:"usageDetail"`
	TotalTokens       int64  `json:"totalTokens"`
	Resolution        string `json:"resolution"`
	Quantity          int    `json:"quantity"`
	// TotalCost is emitted as a JSON number with exact decimal digits;
	// json.Number avoids both float rounding and scientific notation.
	TotalCost json.Number `json:"totalCost"`
	Currency  string      `json:"currency"`
}

// ConsumesData echoes the query back alongside the page of items, as the spec
// requires, so the caller can verify what the server actually applied.
type ConsumesData struct {
	Items       []ConsumeItem `json:"items"`
	BeginTime   int64         `json:"beginTime"`
	EndTime     int64         `json:"endTime"`
	ApiKeyId    string        `json:"apiKeyId"`
	BeginCursor int64         `json:"beginCursor"`
	Limit       int           `json:"limit"`
	Total       int64         `json:"total"`
}

// BalanceData is the GET /api/v1/balance payload.
type BalanceData struct {
	Balance              json.Number `json:"balance"`
	BalanceLastUpdatedAt int64       `json:"balanceLastUpdatedAt"`
	Currency             string      `json:"currency"`
}
