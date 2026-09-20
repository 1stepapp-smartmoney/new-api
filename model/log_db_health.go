package model

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// logDBWriteReportInterval bounds how often a sustained log database outage
// reports itself. Every failed write already logs its own line, but on a busy
// deployment the access-log firehose rotates those out of the container log
// within minutes, so a two-day outage left no evidence behind (fork §11).
const logDBWriteReportInterval = 30 * time.Second

// LogDBWriteHealth reports whether the log database is accepting writes.
// Log writes are best-effort — a failure never fails the caller's request — so
// this is the only signal that the billing log has silently gone incomplete.
type LogDBWriteHealth struct {
	Healthy             bool   `json:"healthy"`
	ConsecutiveFailures int64  `json:"consecutive_failures"`
	TotalFailures       int64  `json:"total_failures"`
	FailingSince        int64  `json:"failing_since"`
	LastFailureAt       int64  `json:"last_failure_at"`
	LastSuccessAt       int64  `json:"last_success_at"`
	LastError           string `json:"last_error"`
}

var (
	logDBWriteMu          sync.Mutex
	logDBWriteState       = LogDBWriteHealth{Healthy: true}
	logDBWriteReportedAt  time.Time
	logDBWriteSinceReport int64
)

// noteLogDBWrite records the outcome of a log database write. A sustained
// outage reports at most once per logDBWriteReportInterval so operators get one
// durable, greppable line per interval instead of one per dropped request.
func noteLogDBWrite(err error) {
	now := common.GetTimestamp()
	var recovery, report string

	logDBWriteMu.Lock()
	if err == nil {
		if !logDBWriteState.Healthy {
			recovery = fmt.Sprintf("log database write recovered: %d log writes were lost over %d seconds",
				logDBWriteState.ConsecutiveFailures, now-logDBWriteState.FailingSince)
		}
		logDBWriteState.Healthy = true
		logDBWriteState.ConsecutiveFailures = 0
		logDBWriteState.FailingSince = 0
		logDBWriteState.LastSuccessAt = now
		logDBWriteReportedAt = time.Time{}
		logDBWriteSinceReport = 0
	} else {
		if logDBWriteState.Healthy {
			logDBWriteState.Healthy = false
			logDBWriteState.FailingSince = now
		}
		logDBWriteState.ConsecutiveFailures++
		logDBWriteState.TotalFailures++
		logDBWriteState.LastFailureAt = now
		logDBWriteState.LastError = err.Error()
		logDBWriteSinceReport++
		if wall := time.Now(); logDBWriteReportedAt.IsZero() || wall.Sub(logDBWriteReportedAt) >= logDBWriteReportInterval {
			logDBWriteReportedAt = wall
			report = fmt.Sprintf(
				"log database write failing: %d dropped since the last report, %d consecutive, failing for %ds; billing logs are being lost, check the log database disk space; last error: %v",
				logDBWriteSinceReport, logDBWriteState.ConsecutiveFailures, now-logDBWriteState.FailingSince, err)
			logDBWriteSinceReport = 0
		}
	}
	logDBWriteMu.Unlock()

	if recovery != "" {
		common.SysLog(recovery)
	}
	if report != "" {
		common.SysError(report)
	}
}

// GetLogDBWriteHealth returns the current log database write health so the
// admin status endpoint can expose it to external monitoring.
func GetLogDBWriteHealth() LogDBWriteHealth {
	logDBWriteMu.Lock()
	defer logDBWriteMu.Unlock()
	return logDBWriteState
}
