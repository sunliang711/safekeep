package backup

import (
	"context"
	"fmt"
	"net/http"

	"safekeep/internal/db"
)

// ReserveRemoteTraffic 让主动拉取的归档复用每日配额及熔断邮件通知。
func (s *Service) ReserveRemoteTraffic(ctx context.Context, filename string, size int64) error {
	settings, err := s.store.GetSystemSettings(ctx)
	if err != nil {
		return err
	}
	if settings.DailyTrafficLimitBytes <= 0 {
		return nil
	}
	allowed, shouldNotify, used, err := s.store.CheckAndReserveDailyTraffic(ctx, db.TodayDateKey(), size, settings.DailyTrafficLimitBytes)
	if err != nil {
		return err
	}
	if allowed {
		return nil
	}
	if shouldNotify {
		s.notifyCircuitBreakerTripped(nil, filename, size, used, settings.DailyTrafficLimitBytes)
	}
	return &HTTPError{Status: http.StatusServiceUnavailable, Code: "circuit_breaker_open", Message: fmt.Sprintf("daily backup quota exceeded (%d / %d bytes), service circuit breaker tripped", used, settings.DailyTrafficLimitBytes)}
}
