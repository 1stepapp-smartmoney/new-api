package controller

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestReconciliationAPI covers the supplier reconciliation contract (fork §10):
// the correlation id the caller reconciles on, offset-cursor paging, the window
// guards, money conversion and key authentication. It runs on every supported
// primary/log database because the paging order and the platform_request_id
// column are database-visible behaviour.
func TestReconciliationAPI(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver, logDriver gorm.Dialector
			dbType := common.DatabaseTypeSQLite
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
				logDriver = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
				logDSN := os.Getenv("TEST_MYSQL_LOG_DSN")
				if logDSN == "" {
					logDSN = dsn
				}
				logDriver = mysql.Open(logDSN)
				dbType = common.DatabaseTypeMySQL
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
				logDSN := os.Getenv("TEST_POSTGRES_LOG_DSN")
				if logDSN == "" {
					logDSN = dsn
				}
				logDriver = postgres.Open(logDSN)
				dbType = common.DatabaseTypePostgreSQL
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

			logDB, err := gorm.Open(logDriver, &gorm.Config{})
			require.NoError(t, err)
			logSQL, err := logDB.DB()
			require.NoError(t, err)
			logSQL.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, logSQL.Close()) })

			previousDB, previousLogDB := model.DB, model.LOG_DB
			previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
			previousRedis := common.RedisEnabled
			model.DB, model.LOG_DB = db, logDB
			common.SetDatabaseTypes(dbType, dbType)
			model.InitColumnQuoting()
			common.RedisEnabled = false
			t.Cleanup(func() {
				model.DB, model.LOG_DB = previousDB, previousLogDB
				common.SetDatabaseTypes(previousMain, previousLog)
				common.RedisEnabled = previousRedis
			})

			for _, table := range []any{&model.User{}, &model.Token{}} {
				require.False(t, db.Migrator().HasTable(table), "use an empty test database")
				require.NoError(t, db.AutoMigrate(table))
				t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(table)) })
			}
			require.False(t, logDB.Migrator().HasTable(&model.Log{}), "use an empty test log database")
			require.NoError(t, logDB.AutoMigrate(&model.Log{}))
			t.Cleanup(func() { require.NoError(t, logDB.Migrator().DropTable(&model.Log{})) })

			user := model.User{Username: "recon-user", Password: "unused", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", Quota: 1_000_000}
			require.NoError(t, db.Create(&user).Error)
			// A reconciliation key is an ordinary token created with no quota:
			// the relay path refuses an exhausted token, so it can never call a
			// model, and these endpoints consume nothing.
			const reconKey = "reconkey00000000000000000000000000"
			reconToken := model.Token{UserId: user.Id, Key: reconKey, Name: "reconciliation", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: false, RemainQuota: 0, UsedQuota: 0}
			require.NoError(t, db.Create(&reconToken).Error)
			otherToken := model.Token{UserId: user.Id, Key: "otherkey0000000000000000000000000", Name: "relay", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}
			require.NoError(t, db.Create(&otherToken).Error)
			// An ordinary relay key that ran out of quota: zero remaining, but it
			// has billed before, so it must not become a reconciliation key.
			exhaustedToken := model.Token{UserId: user.Id, Key: "exhausted00000000000000000000000", Name: "spent", Status: common.TokenStatusExhausted, ExpiredTime: -1, UnlimitedQuota: false, RemainQuota: 0, UsedQuota: 4242}
			require.NoError(t, db.Create(&exhaustedToken).Error)

			const base int64 = 1_780_000_000
			// Anthropic semantics: prompt_tokens excludes both cache reads and
			// cache writes, exactly as the Claude adaptor stores them.
			rows := []model.Log{
				{UserId: user.Id, CreatedAt: base + 1, Type: model.LogTypeConsume, ModelName: "claude-opus-4-7", TokenId: reconToken.Id, TokenName: reconToken.Name, Quota: 500, PromptTokens: 100, CompletionTokens: 20, IsStream: true, RequestId: "gw-1", PlatformRequestId: "platform-1", UpstreamRequestId: "up-1", Other: `{"request_path":"/v1/messages","claude":true,"cache_tokens":30,"cache_creation_tokens":50,"cache_creation_tokens_5m":50}`},
				{UserId: user.Id, CreatedAt: base + 2, Type: model.LogTypeConsume, ModelName: "gpt-5", TokenId: otherToken.Id, TokenName: otherToken.Name, Quota: 250, PromptTokens: 10, CompletionTokens: 5, RequestId: "gw-2", Other: `{"request_path":"/v1/chat/completions","model_price":0.02}`},
				{UserId: user.Id, CreatedAt: base + 3, Type: model.LogTypeConsume, ModelName: "gpt-5", TokenId: reconToken.Id, TokenName: reconToken.Name, Quota: 125, PromptTokens: 1, CompletionTokens: 1, RequestId: "gw-3", Other: `{"request_path":"/v1/chat/completions"}`},
				// Non-consume and out-of-window rows must never be billed back.
				{UserId: user.Id, CreatedAt: base + 4, Type: model.LogTypeError, ModelName: "gpt-5", TokenId: reconToken.Id, TokenName: reconToken.Name, RequestId: "gw-err"},
				{UserId: user.Id, CreatedAt: base + 9999, Type: model.LogTypeConsume, ModelName: "gpt-5", TokenId: reconToken.Id, TokenName: reconToken.Name, Quota: 900, RequestId: "gw-late"},
			}
			require.NoError(t, logDB.Create(&rows).Error)

			router := gin.New()
			router.Use(middleware.RequestId())
			reconciliation := router.Group("/api")
			reconciliation.Use(middleware.ReconciliationAuth())
			reconciliation.POST("/v3/consumes", GetReconciliationConsumes)
			reconciliation.GET("/v1/balance", GetReconciliationBalance)

			post := func(t *testing.T, key string, body string) (*httptest.ResponseRecorder, dto.ReconciliationEnvelope) {
				t.Helper()
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/api/v3/consumes", strings.NewReader(body))
				request.Header.Set("x-api-key", key)
				request.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(response, request)
				var envelope dto.ReconciliationEnvelope
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &envelope))
				return response, envelope
			}
			decodeConsumes := func(t *testing.T, envelope dto.ReconciliationEnvelope) dto.ConsumesData {
				t.Helper()
				encoded, err := common.Marshal(envelope.Data)
				require.NoError(t, err)
				var data dto.ConsumesData
				require.NoError(t, common.Unmarshal(encoded, &data))
				return data
			}

			t.Run("authentication", func(t *testing.T) {
				for _, key := range []string{"", "not-a-real-key"} {
					_, envelope := post(t, key, `{"beginTime":1,"endTime":2}`)
					assert.Equal(t, dto.ReconCodeUnauthorized, envelope.Code)
					assert.NotEmpty(t, envelope.RequestId, "every response carries a request id for support")
				}
				// Valid credentials that are not reconciliation keys must be
				// refused: an ordinary relay key, and — the case that makes
				// used_quota part of the marker — one that merely ran out.
				for _, key := range []string{"otherkey0000000000000000000000000", "exhausted00000000000000000000000"} {
					_, envelope := post(t, key, `{"beginTime":1,"endTime":2}`)
					assert.Equal(t, dto.ReconCodeForbidden, envelope.Code, "only a zero-quota, never-billed key reconciles")
				}
			})

			t.Run("window guards", func(t *testing.T) {
				cases := []struct {
					body string
					code int
				}{
					{`{"beginTime":0,"endTime":0}`, dto.ReconCodeInvalidParam},
					{fmt.Sprintf(`{"beginTime":%d,"endTime":%d}`, base+10, base), dto.ReconCodeInvalidTimeSpan},
					{fmt.Sprintf(`{"beginTime":%d,"endTime":%d}`, base, base+24*60*60+1), dto.ReconCodeInvalidTimeSpan},
					{fmt.Sprintf(`{"beginTime":%d,"endTime":%d,"beginCursor":-1}`, base, base+10), dto.ReconCodeInvalidParam},
				}
				for _, testCase := range cases {
					_, envelope := post(t, reconKey, testCase.body)
					assert.Equal(t, testCase.code, envelope.Code, testCase.body)
				}
			})

			t.Run("items and correlation id", func(t *testing.T) {
				_, envelope := post(t, reconKey, fmt.Sprintf(`{"beginTime":%d,"endTime":%d}`, base, base+100))
				require.Equal(t, dto.ReconCodeSuccess, envelope.Code)
				data := decodeConsumes(t, envelope)
				require.EqualValues(t, 3, data.Total, "only in-window consume rows are billable")
				require.Len(t, data.Items, 3)

				first := data.Items[0]
				assert.Equal(t, "platform-1", first.RequestId, "the caller's own id wins when supplied")
				assert.Equal(t, "up-1", first.UpstreamRequestId)
				assert.Equal(t, reconToken.Name, first.ApiKeyId, "apiKeyId is the key name operators see")
				assert.Equal(t, base+1, first.ConsumeAt)
				assert.True(t, first.Stream)
				assert.EqualValues(t, 120, first.TotalTokens)
				assert.Equal(t, "USD", first.Currency)
				assert.Equal(t, json.Number("0.001"), first.TotalCost, "500 quota over 500000 units per USD")
				usage, ok := first.UsageDetail.(map[string]any)
				require.True(t, ok)
				assert.EqualValues(t, 100, usage["input_tokens"], "Anthropic input_tokens excludes cache")
				assert.EqualValues(t, 20, usage["output_tokens"])
				assert.EqualValues(t, 30, usage["cache_read_input_tokens"])
				assert.EqualValues(t, 50, usage["cache_creation_input_tokens"])
				creation, ok := usage["cache_creation"].(map[string]any)
				require.True(t, ok)
				assert.EqualValues(t, 50, creation["ephemeral_5m_input_tokens"])
				assert.NotContains(t, usage, "prompt_tokens", "the caller asked over the Anthropic API")

				assert.Equal(t, "gw-2", data.Items[1].RequestId, "without a caller id the gateway id is the anchor")
				chatUsage, ok := data.Items[1].UsageDetail.(map[string]any)
				require.True(t, ok)
				assert.EqualValues(t, 10, chatUsage["prompt_tokens"], "OpenAI prompt_tokens keeps cache as a subset")
				assert.EqualValues(t, 5, chatUsage["completion_tokens"])
				assert.EqualValues(t, 15, chatUsage["total_tokens"])
				assert.Equal(t, "per_call", data.Items[1].PriceType, "a positive model_price means per-request billing")
				assert.Equal(t, "token", data.Items[2].PriceType)
				// model_price is written on every text consume log, so a zero
				// value must still read as token billing.
				assert.Equal(t, "token", consumePriceType(map[string]any{"model_price": float64(0)}))
				assert.Equal(t, "token", consumePriceType(map[string]any{}))
			})

			t.Run("offset cursor paging", func(t *testing.T) {
				_, envelope := post(t, reconKey, fmt.Sprintf(`{"beginTime":%d,"endTime":%d,"limit":2}`, base, base+100))
				page := decodeConsumes(t, envelope)
				require.Len(t, page.Items, 2)
				assert.EqualValues(t, 1, page.Items[0].Cursor)
				assert.EqualValues(t, 2, page.Items[1].Cursor)
				assert.EqualValues(t, 3, page.Total)

				_, envelope = post(t, reconKey, fmt.Sprintf(`{"beginTime":%d,"endTime":%d,"limit":2,"beginCursor":%d}`, base, base+100, page.Items[1].Cursor))
				next := decodeConsumes(t, envelope)
				require.Len(t, next.Items, 1, "paging resumes after the last returned cursor")
				assert.Equal(t, "gw-3", next.Items[0].RequestId)
				assert.EqualValues(t, 3, next.Items[0].Cursor)

				_, envelope = post(t, reconKey, fmt.Sprintf(`{"beginTime":%d,"endTime":%d,"beginCursor":3}`, base, base+100))
				assert.Empty(t, decodeConsumes(t, envelope).Items, "an exhausted cursor terminates the walk")
			})

			t.Run("apiKeyId filter", func(t *testing.T) {
				_, envelope := post(t, reconKey, fmt.Sprintf(`{"beginTime":%d,"endTime":%d,"apiKeyId":%q}`, base, base+100, otherToken.Name))
				data := decodeConsumes(t, envelope)
				require.Len(t, data.Items, 1)
				assert.Equal(t, "gw-2", data.Items[0].RequestId)
				assert.EqualValues(t, 1, data.Total)
			})

			t.Run("usage detail follows the caller's protocol", func(t *testing.T) {
				// Same upstream numbers, four different caller-facing APIs. The
				// stored counts carry the upstream provider's semantics, so the
				// Anthropic reading (cache excluded) and the OpenAI reading
				// (cache as a subset) must both come out right.
				claudeLog := &model.Log{PromptTokens: 100, CompletionTokens: 20}
				claudeOther := map[string]any{"claude": true, "cache_tokens": float64(30), "cache_creation_tokens": float64(50)}
				openAILog := &model.Log{PromptTokens: 180, CompletionTokens: 20}
				openAIOther := map[string]any{"cache_tokens": float64(30)}

				anthropic := buildUsageDetail(claudeLog, mergeOther(claudeOther, "/v1/messages"))
				assert.EqualValues(t, 100, anthropic["input_tokens"])
				assert.EqualValues(t, 30, anthropic["cache_read_input_tokens"])
				assert.EqualValues(t, 50, anthropic["cache_creation_input_tokens"])

				// Claude upstream reached over the OpenAI API: prompt_tokens has
				// to be widened back to include the cache, or the caller sees a
				// smaller input than it was billed for.
				chat := buildUsageDetail(claudeLog, mergeOther(claudeOther, "/v1/chat/completions"))
				assert.EqualValues(t, 180, chat["prompt_tokens"])
				assert.EqualValues(t, 200, chat["total_tokens"])
				promptDetails, ok := chat["prompt_tokens_details"].(map[string]any)
				require.True(t, ok)
				assert.EqualValues(t, 30, promptDetails["cached_tokens"])

				// OpenAI upstream over the Responses API: already inclusive.
				responses := buildUsageDetail(openAILog, mergeOther(openAIOther, "/v1/responses"))
				assert.EqualValues(t, 180, responses["input_tokens"])
				assert.EqualValues(t, 200, responses["total_tokens"])
				inputDetails, ok := responses["input_tokens_details"].(map[string]any)
				require.True(t, ok)
				assert.EqualValues(t, 30, inputDetails["cached_tokens"])

				gemini := buildUsageDetail(openAILog, mergeOther(openAIOther, "/v1beta/models/gemini-3-flash:generateContent"))
				assert.EqualValues(t, 180, gemini["promptTokenCount"])
				assert.EqualValues(t, 20, gemini["candidatesTokenCount"])
				assert.EqualValues(t, 200, gemini["totalTokenCount"])
				assert.EqualValues(t, 30, gemini["cachedContentTokenCount"])

				// The Gemini OpenAI-compatible surface answers in OpenAI shape.
				compatible := buildUsageDetail(openAILog, mergeOther(openAIOther, "/v1beta/openai/chat/completions"))
				assert.Contains(t, compatible, "prompt_tokens")
				assert.NotContains(t, compatible, "promptTokenCount")
			})

			t.Run("balance", func(t *testing.T) {
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, "/api/v1/balance", nil)
				request.Header.Set("x-api-key", reconKey)
				router.ServeHTTP(response, request)
				var envelope dto.ReconciliationEnvelope
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &envelope))
				require.Equal(t, dto.ReconCodeSuccess, envelope.Code)
				encoded, err := common.Marshal(envelope.Data)
				require.NoError(t, err)
				var balance dto.BalanceData
				require.NoError(t, common.Unmarshal(encoded, &balance))
				assert.Equal(t, json.Number("2"), balance.Balance, "1000000 quota over 500000 units per USD")
				assert.Equal(t, "USD", balance.Currency)
				assert.EqualValues(t, base+9999, balance.BalanceLastUpdatedAt, "the newest billing event moved the balance")
			})
		})
	}
}

// mergeOther clones the decoded log metadata with the request path that
// identifies which API family the caller used.
func mergeOther(base map[string]any, requestPath string) map[string]any {
	merged := map[string]any{"request_path": requestPath}
	maps.Copy(merged, base)
	return merged
}
