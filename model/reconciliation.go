package model

import (
	"gorm.io/gorm"
)

// ReconciliationConsumeQuery selects one page of billed calls for the supplier
// reconciliation API (fork §10).
type ReconciliationConsumeQuery struct {
	UserId    int
	BeginTime int64
	EndTime   int64
	TokenName string // empty means every key of the user
	Offset    int64  // rows already consumed by the caller, the spec's beginCursor
	Limit     int
}

// reconciliationOrder is the deterministic ordering the offset cursor depends
// on. created_at alone is not unique (multiple calls land on the same second),
// so request_id breaks ties. It matches the ClickHouse table's ORDER BY key
// (created_at, request_id), so paging stays index-ordered there too.
const reconciliationOrder = "created_at asc, request_id asc"

// GetReconciliationConsumeLogs returns the page plus the total row count for the
// whole window, which the caller needs to decide whether to keep paging.
func GetReconciliationConsumeLogs(query ReconciliationConsumeQuery) (logs []*Log, total int64, err error) {
	build := func() *gorm.DB {
		tx := LOG_DB.Model(&Log{}).
			Where("type = ?", LogTypeConsume).
			Where("user_id = ?", query.UserId).
			Where("created_at >= ?", query.BeginTime).
			Where("created_at <= ?", query.EndTime)
		if query.TokenName != "" {
			tx = tx.Where("token_name = ?", query.TokenName)
		}
		return tx
	}

	if err = build().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total == 0 || query.Offset >= total {
		return []*Log{}, total, nil
	}
	err = build().Order(reconciliationOrder).
		Offset(int(query.Offset)).
		Limit(query.Limit).
		Find(&logs).Error
	if err != nil {
		return nil, 0, err
	}
	return logs, total, nil
}

// GetLastBillingActivityAt reports when the user's balance last moved, which the
// balance endpoint returns as balanceLastUpdatedAt. Consume, top-up and refund
// rows are the events that change it; 0 means the user has no billing history.
func GetLastBillingActivityAt(userId int) (int64, error) {
	var newest struct {
		CreatedAt int64
	}
	err := LOG_DB.Model(&Log{}).
		Select("created_at").
		Where("user_id = ?", userId).
		Where("type in ?", []int{LogTypeConsume, LogTypeTopup, LogTypeRefund}).
		Order("created_at desc").
		Limit(1).
		Scan(&newest).Error
	if err != nil {
		return 0, err
	}
	return newest.CreatedAt, nil
}
