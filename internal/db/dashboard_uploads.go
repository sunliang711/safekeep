package db

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type UploadCount struct {
	ClientID    string `json:"client_id"`
	ClientName  string `json:"client_name"`
	UploadCount int64  `json:"upload_count"`
}

type UploadStats struct {
	Today    []UploadCount `json:"today"`
	Week     []UploadCount `json:"week"`
	Month    []UploadCount `json:"month"`
	Timezone string        `json:"timezone"`
}

func (s *Store) GetAPIUploadStats(ctx context.Context, now time.Time) (*UploadStats, error) {
	stats := &UploadStats{
		Today:    []UploadCount{},
		Week:     []UploadCount{},
		Month:    []UploadCount{},
		Timezone: now.Format("MST (UTC-07:00)"),
	}
	// 按服务端本地日历计算边界，再转成 UTC 与审计日志的时间比较。
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	daysSinceMonday := (int(today.Weekday()) + 6) % 7
	week := today.AddDate(0, 0, -daysSinceMonday)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	start := month
	if week.Before(start) {
		start = week
	}

	// 同一客户始终按 ID 聚合；排除幂等重放，并按任务 ID 避免重复计数。
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.actor_id,
			COALESCE(NULLIF(c.name, ''), NULLIF(MAX(a.actor_name), ''), a.actor_id),
			COUNT(DISTINCT CASE WHEN a.created_at >= ? THEN a.resource_id END),
			COUNT(DISTINCT CASE WHEN a.created_at >= ? THEN a.resource_id END),
			COUNT(DISTINCT CASE WHEN a.created_at >= ? THEN a.resource_id END)
		FROM audit_logs a
		LEFT JOIN clients c ON c.id = a.actor_id
		WHERE a.action = 'backup.upload' AND a.actor_type = 'client'
			AND a.status = 'success' AND a.resource_type = 'backup_task' AND a.resource_id != ''
			AND a.created_at >= ? AND a.created_at <= ?
			AND CASE WHEN json_valid(a.detail_json)
				THEN COALESCE(json_extract(a.detail_json, '$.idempotent_replay'), 0) = 0
					AND COALESCE(json_extract(a.detail_json, '$.task_status'), '') != 'failed'
				ELSE 1 END
		GROUP BY a.actor_id, c.name`,
		today.UTC(), week.UTC(), month.UTC(), start.UTC(), now.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("query API upload statistics: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var clientID, clientName string
		var todayCount, weekCount, monthCount int64
		if err := rows.Scan(&clientID, &clientName, &todayCount, &weekCount, &monthCount); err != nil {
			return nil, fmt.Errorf("scan API upload statistics: %w", err)
		}
		if todayCount > 0 {
			stats.Today = append(stats.Today, UploadCount{clientID, clientName, todayCount})
		}
		if weekCount > 0 {
			stats.Week = append(stats.Week, UploadCount{clientID, clientName, weekCount})
		}
		if monthCount > 0 {
			stats.Month = append(stats.Month, UploadCount{clientID, clientName, monthCount})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate API upload statistics: %w", err)
	}
	for _, counts := range [][]UploadCount{stats.Today, stats.Week, stats.Month} {
		sort.Slice(counts, func(i, j int) bool {
			if counts[i].UploadCount == counts[j].UploadCount {
				return counts[i].ClientID < counts[j].ClientID
			}
			return counts[i].UploadCount > counts[j].UploadCount
		})
	}
	return stats, nil
}
