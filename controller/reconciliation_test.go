package controller

import (
	"encoding/json"
	"fmt"
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
			// A reconciliation key is an ordinary token restricted with the
			// existing controls: unlimited quota so it is never "exhausted",
			// and an empty model allow-list so no relay call can pass.
			const reconKey = "reconkey00000000000000000000000000"
			reconToken := model.Token{UserId: user.Id, Key: reconKey, Name: "reconciliation", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true, ModelLimitsEnabled: true, ModelLimits: ""}
			require.NoError(t, db.Create(&reconToken).Error)
			otherToken := model.Token{UserId: user.Id, Key: "otherkey0000000000000000000000000", Name: "relay", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}
			require.NoError(t, db.Create(&otherToken).Error)

			// The empty allow-list must deny every model; that is what makes the
			// key reconciliation-only without a new permission flag.
			assert.Empty(t, reconToken.GetModelLimitsMap())

			const base int64 = 1_780_000_000
			rows := []model.Log{
				{UserId: user.Id, CreatedAt: base + 1, Type: model.LogTypeConsume, ModelName: "claude-opus-4-7", TokenId: reconToken.Id, Quota: 500, PromptTokens: 100, CompletionTokens: 20, IsStream: true, RequestId: "gw-1", PlatformRequestId: "platform-1", UpstreamRequestId: "up-1", Other: `{"cache_tokens":7,"cache_creation_tokens":3}`},
				{UserId: user.Id, CreatedAt: base + 2, Type: model.LogTypeConsume, ModelName: "gpt-5", TokenId: otherToken.Id, Quota: 250, PromptTokens: 10, CompletionTokens: 5, RequestId: "gw-2", Other: `{"model_price":0.02}`},
				{UserId: user.Id, CreatedAt: base + 3, Type: model.LogTypeConsume, ModelName: "gpt-5", TokenId: reconToken.Id, Quota: 125, PromptTokens: 1, CompletionTokens: 1, RequestId: "gw-3"},
				// Non-consume and out-of-window rows must never be billed back.
				{UserId: user.Id, CreatedAt: base + 4, Type: model.LogTypeError, ModelName: "gpt-5", TokenId: reconToken.Id, RequestId: "gw-err"},
				{UserId: user.Id, CreatedAt: base + 9999, Type: model.LogTypeConsume, ModelName: "gpt-5", TokenId: reconToken.Id, Quota: 900, RequestId: "gw-late"},
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
					{fmt.Sprintf(`{"beginTime":%d,"endTime":%d,"apiKeyId":"abc"}`, base, base+10), dto.ReconCodeInvalidParam},
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
				assert.Equal(t, fmt.Sprint(reconToken.Id), first.ApiKeyId)
				assert.Equal(t, base+1, first.ConsumeAt)
				assert.True(t, first.Stream)
				assert.EqualValues(t, 120, first.TotalTokens)
				assert.Equal(t, "USD", first.Currency)
				assert.Equal(t, json.Number("0.001"), first.TotalCost, "500 quota over 500000 units per USD")
				usage, ok := first.UsageDetail.(map[string]any)
				require.True(t, ok)
				assert.EqualValues(t, 100, usage["input_tokens"])
				assert.EqualValues(t, 20, usage["output_tokens"])
				inputDetails, ok := usage["input_tokens_details"].(map[string]any)
				require.True(t, ok)
				assert.EqualValues(t, 7, inputDetails["cached_tokens"])
				assert.EqualValues(t, 3, inputDetails["cache_write_tokens"])

				assert.Equal(t, "gw-2", data.Items[1].RequestId, "without a caller id the gateway id is the anchor")
				assert.Equal(t, "per_call", data.Items[1].PriceType)
				assert.Equal(t, "token", data.Items[2].PriceType)
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
				_, envelope := post(t, reconKey, fmt.Sprintf(`{"beginTime":%d,"endTime":%d,"apiKeyId":"%d"}`, base, base+100, otherToken.Id))
				data := decodeConsumes(t, envelope)
				require.Len(t, data.Items, 1)
				assert.Equal(t, "gw-2", data.Items[0].RequestId)
				assert.EqualValues(t, 1, data.Total)
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
