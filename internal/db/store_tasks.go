package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// --- Backup Tasks & Items ---

func (s *Store) CreateBackupTask(ctx context.Context, t *BackupTask, items []BackupTaskItem) error {
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.MetadataJSON == "" {
		t.MetadataJSON = "{}"
	}
	if t.WebhookStatus == "" {
		t.WebhookStatus = "none"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO backup_tasks(
			id, client_id, idempotency_key, mode, status,
			original_filename, remote_filename, content_type, file_size, sha256,
			spool_path, spool_cleaned, custom_subpath, metadata_json,
			deduplicated, webhook_status, error_summary,
			created_at, started_at, finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ClientID, t.IdempotencyKey, t.Mode, t.Status,
		t.OriginalFilename, t.RemoteFilename, t.ContentType, t.FileSize, t.SHA256,
		t.SpoolPath, boolToInt(t.SpoolCleaned), t.CustomSubpath, t.MetadataJSON,
		boolToInt(t.Deduplicated), t.WebhookStatus, t.ErrorSummary,
		t.CreatedAt, t.StartedAt, t.FinishedAt,
	); err != nil {
		return err
	}

	for i := range items {
		it := &items[i]
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO backup_task_items(
				id, task_id, upstream_id, upstream_name, upstream_type,
				remote_path, remote_etag, remote_version_id, status,
				attempt_count, max_attempts, next_retry_at, duration_ms,
				error_message, started_at, finished_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			it.ID, t.ID, it.UpstreamID, it.UpstreamName, it.UpstreamType,
			it.RemotePath, it.RemoteETag, it.RemoteVersionID, it.Status,
			it.AttemptCount, it.MaxAttempts, it.NextRetryAt, it.DurationMs,
			it.ErrorMessage, it.StartedAt, it.FinishedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetTaskByIdempotencyKey(ctx context.Context, clientID, key string) (*BackupTask, error) {
	if key == "" {
		return nil, ErrNotFound
	}
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM backup_tasks WHERE client_id = ? AND idempotency_key = ?`,
		clientID, key,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.GetBackupTask(ctx, id)
}

// FindSucceededItemsByHash looks up previously succeeded backup items for the same client and SHA-256 hash.
// It returns a map keyed by upstream_id with the latest succeeded item metadata.
func (s *Store) FindSucceededItemsByHash(ctx context.Context, clientID, sha256Hex string) (map[string]BackupTaskItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT i.id, i.task_id, i.upstream_id, i.upstream_name, i.upstream_type,
			i.remote_path, i.remote_etag, i.remote_version_id, i.status,
			i.attempt_count, i.max_attempts, i.next_retry_at, i.duration_ms,
			i.error_message, i.started_at, i.finished_at
		 FROM backup_task_items i
		 JOIN backup_tasks t ON t.id = i.task_id
		 WHERE t.client_id = ? AND t.sha256 = ? AND i.status = 'succeeded'
		 ORDER BY i.finished_at DESC, t.created_at DESC`,
		clientID, sha256Hex,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]BackupTaskItem)
	for rows.Next() {
		it, err := scanTaskItem(rows)
		if err != nil {
			return nil, err
		}
		if _, exists := result[it.UpstreamID]; !exists {
			result[it.UpstreamID] = *it
		}
	}
	return result, rows.Err()
}

func (s *Store) GetBackupTask(ctx context.Context, id string) (*BackupTask, error) {
	t, err := scanBackupTask(s.db.QueryRowContext(ctx,
		`SELECT t.id, t.client_id, COALESCE(c.name, ''), t.idempotency_key, t.mode, t.status,
			t.original_filename, t.remote_filename, t.content_type, t.file_size, t.sha256,
			t.spool_path, t.spool_cleaned, t.custom_subpath, t.metadata_json,
			t.deduplicated, t.webhook_status, t.error_summary,
			t.created_at, t.started_at, t.finished_at
		 FROM backup_tasks t
		 LEFT JOIN clients c ON c.id = t.client_id
		 WHERE t.id = ?`, id))
	if err != nil {
		return nil, err
	}
	items, err := s.ListTaskItems(ctx, id)
	if err != nil {
		return nil, err
	}
	t.Items = items
	return t, nil
}

func (s *Store) ListTaskItems(ctx context.Context, taskID string) ([]BackupTaskItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, upstream_id, upstream_name, upstream_type,
			remote_path, remote_etag, remote_version_id, status,
			attempt_count, max_attempts, next_retry_at, duration_ms,
			error_message, started_at, finished_at
		 FROM backup_task_items WHERE task_id = ? ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []BackupTaskItem
	for rows.Next() {
		it, err := scanTaskItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *it)
	}
	return items, rows.Err()
}

type TaskFilter struct {
	ClientID string
	Status   string
	Mode     string
	Query    string
	Limit    int
	Offset   int
}

func (s *Store) ListBackupTasks(ctx context.Context, f TaskFilter) ([]BackupTask, int64, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 20
	}
	where := []string{"1=1"}
	var args []any
	if f.ClientID != "" {
		where = append(where, "t.client_id = ?")
		args = append(args, f.ClientID)
	}
	if f.Status != "" {
		where = append(where, "t.status = ?")
		args = append(args, f.Status)
	}
	if f.Mode != "" {
		where = append(where, "t.mode = ?")
		args = append(args, f.Mode)
	}
	if f.Query != "" {
		q := "%" + f.Query + "%"
		where = append(where, "(t.original_filename LIKE ? OR t.remote_filename LIKE ? OR t.sha256 LIKE ? OR t.id LIKE ?)")
		args = append(args, q, q, q, q)
	}
	clause := strings.Join(where, " AND ")

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_tasks t WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	queryArgs := append(append([]any{}, args...), f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.id, t.client_id, COALESCE(c.name, ''), t.idempotency_key, t.mode, t.status,
			t.original_filename, t.remote_filename, t.content_type, t.file_size, t.sha256,
			t.spool_path, t.spool_cleaned, t.custom_subpath, t.metadata_json,
			t.deduplicated, t.webhook_status, t.error_summary,
			t.created_at, t.started_at, t.finished_at
		 FROM backup_tasks t
		 LEFT JOIN clients c ON c.id = t.client_id
		 WHERE `+clause+`
		 ORDER BY t.created_at DESC
		 LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var tasks []BackupTask
	for rows.Next() {
		t, err := scanBackupTask(rows)
		if err != nil {
			return nil, 0, err
		}
		tasks = append(tasks, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for i := range tasks {
		items, err := s.ListTaskItems(ctx, tasks[i].ID)
		if err != nil {
			return nil, 0, err
		}
		tasks[i].Items = items
	}
	return tasks, total, nil
}

func (s *Store) UpdateTaskStatus(ctx context.Context, id, status, errSummary string, startedAt, finishedAt *time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE backup_tasks SET status = ?, error_summary = ?, started_at = COALESCE(started_at, ?), finished_at = ? WHERE id = ?`,
		status, errSummary, startedAt, finishedAt, id,
	)
	return err
}

func (s *Store) MarkSpoolCleaned(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backup_tasks SET spool_cleaned = 1 WHERE id = ?`, id)
	return err
}

func (s *Store) UpdateTaskWebhookStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backup_tasks SET webhook_status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) UpdateTaskItemResult(ctx context.Context, it *BackupTaskItem) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE backup_task_items SET
			remote_path = ?, status = ?, attempt_count = ?, next_retry_at = ?, duration_ms = ?,
			remote_etag = ?, remote_version_id = ?, error_message = ?,
			started_at = ?, finished_at = ?
		 WHERE id = ?`,
		it.RemotePath, it.Status, it.AttemptCount, it.NextRetryAt, it.DurationMs,
		it.RemoteETag, it.RemoteVersionID, it.ErrorMessage,
		it.StartedAt, it.FinishedAt, it.ID,
	)
	return err
}

// ResetStaleRunning recovers interrupted tasks/items on startup.
func (s *Store) ResetStaleRunning(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE backup_task_items SET status = 'pending', next_retry_at = NULL WHERE status = 'running'`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE backup_tasks SET status = 'pending' WHERE status = 'running'`); err != nil {
		return err
	}
	return nil
}

// FindRunnableTaskIDs finds tasks with pending items ready to execute or retry.
// Tasks currently in 'running' state are excluded to avoid racing with active sync/worker execution.
func (s *Store) FindRunnableTaskIDs(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 20
	}
	now := time.Now().UTC()
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT t.id
		 FROM backup_tasks t
		 JOIN backup_task_items i ON i.task_id = t.id
		 WHERE t.status IN ('pending', 'partial_success')
		   AND i.status = 'pending'
		   AND (i.next_retry_at IS NULL OR i.next_retry_at <= ?)
		 ORDER BY t.created_at ASC
		 LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RequeueFailedItems resets failed items of a task back to pending for manual retry.
func (s *Store) RequeueFailedItems(ctx context.Context, taskID string, maxAttempts int) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`UPDATE backup_task_items
		 SET status = 'pending', attempt_count = 0, max_attempts = ?, next_retry_at = NULL, error_message = ''
		 WHERE task_id = ? AND status = 'failed'`,
		maxAttempts, taskID,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE backup_tasks SET status = 'pending', finished_at = NULL, error_summary = '' WHERE id = ?`,
			taskID,
		); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// ListActiveSpoolPaths returns spool paths still referenced by non-cleaned incomplete tasks.
func (s *Store) ListActiveSpoolPaths(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT spool_path FROM backup_tasks
		 WHERE spool_cleaned = 0 AND spool_path != '' AND status IN ('pending', 'running', 'partial_success', 'failed')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[string]struct{})
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		set[p] = struct{}{}
	}
	return set, rows.Err()
}

// ListExpiredFailedTaskSpools returns task IDs and spool paths for permanently failed tasks older than cutoff.
func (s *Store) ListExpiredFailedTaskSpools(ctx context.Context, cutoff time.Time) ([]BackupTask, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, spool_path FROM backup_tasks
		 WHERE status = 'failed' AND spool_cleaned = 0 AND spool_path != ''
		   AND COALESCE(finished_at, created_at) < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []BackupTask
	for rows.Next() {
		var t BackupTask
		if err := rows.Scan(&t.ID, &t.SpoolPath); err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, rows.Err()
}

// ExportAllBackupTasks returns all backup tasks and their items without pagination clamping.
func (s *Store) ExportAllBackupTasks(ctx context.Context) ([]BackupTask, []BackupTaskItem, error) {
	const pageSize = 200
	var allTasks []BackupTask
	var allItems []BackupTaskItem
	for offset := 0; ; offset += pageSize {
		batch, _, err := s.ListBackupTasks(ctx, TaskFilter{Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, nil, err
		}
		for _, t := range batch {
			items := t.Items
			t.Items = nil
			allTasks = append(allTasks, t)
			allItems = append(allItems, items...)
		}
		if len(batch) < pageSize {
			break
		}
	}
	return allTasks, allItems, nil
}

// ExportAllAuditLogs returns all audit logs matching the filter without pagination clamping.
func (s *Store) ExportAllAuditLogs(ctx context.Context, f AuditFilter) ([]AuditLog, error) {
	const pageSize = 500
	var all []AuditLog
	for offset := 0; ; offset += pageSize {
		f.Limit = pageSize
		f.Offset = offset
		batch, _, err := s.ListAuditLogs(ctx, f)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < pageSize {
			break
		}
	}
	return all, nil
}

func scanBackupTask(row rowScanner) (*BackupTask, error) {
	var t BackupTask
	var cleaned, dedup int
	var startedAt, finishedAt sql.NullTime
	if err := row.Scan(
		&t.ID, &t.ClientID, &t.ClientName, &t.IdempotencyKey, &t.Mode, &t.Status,
		&t.OriginalFilename, &t.RemoteFilename, &t.ContentType, &t.FileSize, &t.SHA256,
		&t.SpoolPath, &cleaned, &t.CustomSubpath, &t.MetadataJSON,
		&dedup, &t.WebhookStatus, &t.ErrorSummary,
		&t.CreatedAt, &startedAt, &finishedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t.SpoolCleaned = cleaned == 1
	t.Deduplicated = dedup == 1
	if startedAt.Valid {
		v := startedAt.Time
		t.StartedAt = &v
	}
	if finishedAt.Valid {
		v := finishedAt.Time
		t.FinishedAt = &v
	}
	return &t, nil
}

func scanTaskItem(row rowScanner) (*BackupTaskItem, error) {
	var it BackupTaskItem
	var nextRetry, startedAt, finishedAt sql.NullTime
	if err := row.Scan(
		&it.ID, &it.TaskID, &it.UpstreamID, &it.UpstreamName, &it.UpstreamType,
		&it.RemotePath, &it.RemoteETag, &it.RemoteVersionID, &it.Status,
		&it.AttemptCount, &it.MaxAttempts, &nextRetry, &it.DurationMs,
		&it.ErrorMessage, &startedAt, &finishedAt,
	); err != nil {
		return nil, err
	}
	if nextRetry.Valid {
		v := nextRetry.Time
		it.NextRetryAt = &v
	}
	if startedAt.Valid {
		v := startedAt.Time
		it.StartedAt = &v
	}
	if finishedAt.Valid {
		v := finishedAt.Time
		it.FinishedAt = &v
	}
	return &it, nil
}

// --- Audit Logs ---

func (s *Store) InsertAuditLog(ctx context.Context, l *AuditLog) error {
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now().UTC()
	}
	if l.DetailJSON == "" {
		l.DetailJSON = "{}"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_logs(
			actor_type, actor_id, actor_name, action, resource_type, resource_id,
			status, ip_address, user_agent, request_id, detail_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ActorType, l.ActorID, l.ActorName, l.Action, l.ResourceType, l.ResourceID,
		l.Status, l.IPAddress, l.UserAgent, l.RequestID, l.DetailJSON, l.CreatedAt,
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	l.ID = id
	return nil
}

type AuditFilter struct {
	ActorType string
	Action    string
	Status    string
	Query     string
	From      *time.Time
	To        *time.Time
	Limit     int
	Offset    int
}

func (s *Store) ListAuditLogs(ctx context.Context, f AuditFilter) ([]AuditLog, int64, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	where := []string{"1=1"}
	var args []any
	if f.ActorType != "" {
		where = append(where, "actor_type = ?")
		args = append(args, f.ActorType)
	}
	if f.Action != "" {
		where = append(where, "action LIKE ?")
		args = append(args, "%"+f.Action+"%")
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Query != "" {
		q := "%" + f.Query + "%"
		where = append(where, "(actor_name LIKE ? OR resource_id LIKE ? OR detail_json LIKE ?)")
		args = append(args, q, q, q)
	}
	if f.From != nil {
		where = append(where, "created_at >= ?")
		args = append(args, *f.From)
	}
	if f.To != nil {
		where = append(where, "created_at <= ?")
		args = append(args, *f.To)
	}
	clause := strings.Join(where, " AND ")

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	queryArgs := append(append([]any{}, args...), f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, actor_type, actor_id, actor_name, action, resource_type, resource_id,
			status, ip_address, user_agent, request_id, detail_json, created_at
		 FROM audit_logs WHERE `+clause+`
		 ORDER BY id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var logs []AuditLog
	for rows.Next() {
		var l AuditLog
		if err := rows.Scan(
			&l.ID, &l.ActorType, &l.ActorID, &l.ActorName, &l.Action, &l.ResourceType, &l.ResourceID,
			&l.Status, &l.IPAddress, &l.UserAgent, &l.RequestID, &l.DetailJSON, &l.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		logs = append(logs, l)
	}
	return logs, total, rows.Err()
}

func (s *Store) DeleteAuditLogsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit_logs WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) DeleteCompletedTasksBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM backup_tasks
		 WHERE status IN ('succeeded', 'failed') AND spool_cleaned = 1 AND created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n1, _ := res.RowsAffected()
	res2, err := s.db.ExecContext(ctx, `DELETE FROM cron_job_runs WHERE started_at < ?`, cutoff)
	if err != nil {
		return n1, err
	}
	n2, _ := res2.RowsAffected()
	return n1 + n2, nil
}

// --- Cron Jobs ---

func (s *Store) ListCronJobs(ctx context.Context) ([]CronJob, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, job_key, name, description, cron_expr, enabled, config_json,
			last_run_at, last_status, last_duration_ms, last_summary, next_run_at, updated_at
		 FROM cron_jobs ORDER BY job_key ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []CronJob
	for rows.Next() {
		j, err := scanCronJob(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *j)
	}
	return list, rows.Err()
}

func (s *Store) GetCronJobByID(ctx context.Context, id string) (*CronJob, error) {
	return scanCronJob(s.db.QueryRowContext(ctx,
		`SELECT id, job_key, name, description, cron_expr, enabled, config_json,
			last_run_at, last_status, last_duration_ms, last_summary, next_run_at, updated_at
		 FROM cron_jobs WHERE id = ?`, id))
}

func (s *Store) UpdateCronJob(ctx context.Context, id, cronExpr string, enabled bool, configJSON string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE cron_jobs SET cron_expr = ?, enabled = ?, config_json = ?, updated_at = ? WHERE id = ?`,
		cronExpr, boolToInt(enabled), configJSON, time.Now().UTC(), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateCronJobNextRun(ctx context.Context, id string, nextRun *time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cron_jobs SET next_run_at = ? WHERE id = ?`, nextRun, id)
	return err
}

func (s *Store) UpdateCronJobExecution(ctx context.Context, id, status, summary string, durationMs int64, runAt time.Time, nextRun *time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE cron_jobs SET last_run_at = ?, last_status = ?, last_duration_ms = ?, last_summary = ?, next_run_at = ?, updated_at = ? WHERE id = ?`,
		runAt, status, durationMs, summary, nextRun, time.Now().UTC(), id,
	)
	return err
}

func (s *Store) InsertCronJobRun(ctx context.Context, r *CronJobRun) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cron_job_runs(id, job_id, trigger_type, triggered_by, status, affected_rows, output_log, started_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.JobID, r.TriggerType, r.TriggeredBy, r.Status, r.AffectedRows, r.OutputLog, r.StartedAt, r.FinishedAt,
	)
	return err
}

func (s *Store) FinishCronJobRun(ctx context.Context, id, status string, affectedRows int64, outputLog string, finishedAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE cron_job_runs SET status = ?, affected_rows = ?, output_log = ?, finished_at = ? WHERE id = ?`,
		status, affectedRows, outputLog, finishedAt, id,
	)
	return err
}

func (s *Store) ListCronJobRuns(ctx context.Context, jobID string, limit int) ([]CronJobRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, job_id, trigger_type, triggered_by, status, affected_rows, output_log, started_at, finished_at
		 FROM cron_job_runs WHERE job_id = ? ORDER BY started_at DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []CronJobRun
	for rows.Next() {
		var r CronJobRun
		var fin sql.NullTime
		if err := rows.Scan(&r.ID, &r.JobID, &r.TriggerType, &r.TriggeredBy, &r.Status, &r.AffectedRows, &r.OutputLog, &r.StartedAt, &fin); err != nil {
			return nil, err
		}
		if fin.Valid {
			v := fin.Time
			r.FinishedAt = &v
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

func scanCronJob(row rowScanner) (*CronJob, error) {
	var j CronJob
	var enabled int
	var lastRun, nextRun sql.NullTime
	if err := row.Scan(
		&j.ID, &j.JobKey, &j.Name, &j.Description, &j.CronExpr, &enabled, &j.ConfigJSON,
		&lastRun, &j.LastStatus, &j.LastDurationMs, &j.LastSummary, &nextRun, &j.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	j.Enabled = enabled == 1
	if lastRun.Valid {
		v := lastRun.Time
		j.LastRunAt = &v
	}
	if nextRun.Valid {
		v := nextRun.Time
		j.NextRunAt = &v
	}
	return &j, nil
}

// --- Dashboard Summary ---

func (s *Store) GetDashboardSummary(ctx context.Context) (*DashboardSummary, error) {
	var d DashboardSummary
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN status = 'succeeded' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('failed', 'partial_success') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('pending', 'running') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'succeeded' THEN file_size ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN deduplicated = 1 THEN 1 ELSE 0 END), 0)
		FROM backup_tasks`,
	).Scan(&d.TotalTasks, &d.SucceededTasks, &d.FailedTasks, &d.PendingTasks, &d.TotalBytesBacked, &d.DeduplicatedTasks); err != nil {
		return nil, err
	}

	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM upstreams WHERE enabled = 1`).Scan(&d.EnabledUpstreams)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM upstreams WHERE enabled = 1 AND last_test_status = 'ok'`).Scan(&d.HealthyUpstreams)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM clients WHERE enabled = 1`).Scan(&d.EnabledClients)

	recent, _, err := s.ListBackupTasks(ctx, TaskFilter{Limit: 8})
	if err == nil {
		d.RecentTasks = recent
	}
	ups, err := s.ListUpstreams(ctx)
	if err == nil {
		d.Upstreams = ups
	}
	if settings, err := s.GetSystemSettings(ctx); err == nil {
		if dt, dErr := s.GetDailyTraffic(ctx, TodayDateKey(), settings.DailyTrafficLimitBytes); dErr == nil {
			d.DailyTraffic = dt
		}
	}
	return &d, nil
}

// TodayDateKey returns today's local date string (YYYY-MM-DD) for daily quota tracking.
func TodayDateKey() string {
	return time.Now().Format("2006-01-02")
}

// GetDailyTraffic returns traffic usage and circuit breaker state for the given dateKey.
func (s *Store) GetDailyTraffic(ctx context.Context, dateKey string, limitBytes int64) (*DailyTraffic, error) {
	dt := &DailyTraffic{
		DateKey:    dateKey,
		LimitBytes: limitBytes,
	}
	var trippedAt, notifiedAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT bytes_used, tripped_at, notified_at, updated_at FROM daily_traffic WHERE date_key = ?`,
		dateKey,
	).Scan(&dt.BytesUsed, &trippedAt, &notifiedAt, &dt.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			dt.UpdatedAt = time.Now().UTC()
			return dt, nil
		}
		return nil, err
	}
	if trippedAt.Valid {
		v := trippedAt.Time
		dt.TrippedAt = &v
	}
	if notifiedAt.Valid {
		v := notifiedAt.Time
		dt.NotifiedAt = &v
	}
	dt.Tripped = trippedAt.Valid || (limitBytes > 0 && dt.BytesUsed >= limitBytes)
	return dt, nil
}

// IsCircuitBreakerTripped checks whether the service circuit breaker is currently open for dateKey.
func (s *Store) IsCircuitBreakerTripped(ctx context.Context, dateKey string, limitBytes int64) (bool, int64, error) {
	if limitBytes <= 0 {
		return false, 0, nil
	}
	dt, err := s.GetDailyTraffic(ctx, dateKey, limitBytes)
	if err != nil {
		return false, 0, err
	}
	return dt.Tripped, dt.BytesUsed, nil
}

// CheckAndReserveDailyTraffic atomically checks the daily quota and reserves addBytes if within limit.
// If limitBytes is exceeded, it trips the circuit breaker and returns shouldNotify=true once per trip.
func (s *Store) CheckAndReserveDailyTraffic(ctx context.Context, dateKey string, addBytes, limitBytes int64) (allowed bool, shouldNotify bool, currentUsed int64, err error) {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, 0, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO daily_traffic(date_key, bytes_used, updated_at) VALUES (?, 0, ?)`,
		dateKey, now,
	); err != nil {
		return false, false, 0, err
	}

	var used int64
	var trippedAt, notifiedAt sql.NullTime
	if err := tx.QueryRowContext(ctx,
		`SELECT bytes_used, tripped_at, notified_at FROM daily_traffic WHERE date_key = ?`,
		dateKey,
	).Scan(&used, &trippedAt, &notifiedAt); err != nil {
		return false, false, 0, err
	}

	if limitBytes > 0 && (trippedAt.Valid || used >= limitBytes || used+addBytes > limitBytes) {
		if !trippedAt.Valid {
			if _, err := tx.ExecContext(ctx,
				`UPDATE daily_traffic SET tripped_at = ?, updated_at = ? WHERE date_key = ?`,
				now, now, dateKey,
			); err != nil {
				return false, false, used, err
			}
		}
		if !notifiedAt.Valid {
			res, err := tx.ExecContext(ctx,
				`UPDATE daily_traffic SET notified_at = ?, updated_at = ? WHERE date_key = ? AND notified_at IS NULL`,
				now, now, dateKey,
			)
			if err != nil {
				return false, false, used, err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				shouldNotify = true
			}
		}
		if err := tx.Commit(); err != nil {
			return false, false, used, err
		}
		return false, shouldNotify, used, nil
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE daily_traffic SET bytes_used = bytes_used + ?, updated_at = ? WHERE date_key = ?`,
		addBytes, now, dateKey,
	); err != nil {
		return false, false, used, err
	}
	if err := tx.Commit(); err != nil {
		return false, false, used, err
	}
	return true, false, used + addBytes, nil
}

// ResetDailyCircuitBreaker resets today's traffic counter and clears the tripped state.
func (s *Store) ResetDailyCircuitBreaker(ctx context.Context, dateKey string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO daily_traffic(date_key, bytes_used, tripped_at, notified_at, updated_at)
		 VALUES (?, 0, NULL, NULL, ?)
		 ON CONFLICT(date_key) DO UPDATE SET bytes_used = 0, tripped_at = NULL, notified_at = NULL, updated_at = excluded.updated_at`,
		dateKey, now,
	)
	return err
}
