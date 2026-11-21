package migration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
	"safekeep/internal/db"
)

// 私钥明文只存在于既有的口令加密迁移包内，导入时使用目标环境主密钥重加密。
type RemoteHostRecord struct {
	Host       db.RemoteHost `json:"host"`
	PrivateKey string        `json:"private_key"`
}

func (s *Service) exportRemote(ctx context.Context, data *BundleData, includeRuns bool) error {
	hosts, err := s.store.ListRemoteHosts(ctx)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		plain, err := s.cipher.DecryptString(h.PrivateKeyEncrypted)
		if err != nil {
			return fmt.Errorf("decrypt remote SSH key: %w", err)
		}
		data.RemoteHosts = append(data.RemoteHosts, RemoteHostRecord{Host: h, PrivateKey: plain})
	}
	data.RemoteJobs, err = s.store.ListRemoteJobs(ctx)
	if err != nil {
		return err
	}
	if !includeRuns {
		return nil
	}
	rows, err := s.store.DB().QueryContext(ctx, `SELECT data_json FROM remote_backup_runs ORDER BY started_at`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var rec struct {
			Run db.RemoteRun `json:"run"`
		}
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return err
		}
		data.RemoteRuns = append(data.RemoteRuns, rec.Run)
	}
	return rows.Err()
}

func remoteConflict(ctx context.Context, tx *sql.Tx, table, id, name string) (string, bool, error) {
	// 表名仅由本文件的常量传入。
	for _, column := range []string{"id", "name"} {
		value := id
		if column == "name" {
			value = name
		}
		var found string
		err := tx.QueryRowContext(ctx, `SELECT id FROM `+table+` WHERE `+column+`=?`, value).Scan(&found)
		if err == nil {
			return found, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
	}
	return id, false, nil
}

func (s *Service) importRemote(ctx context.Context, tx *sql.Tx, data BundleData, upstreams map[string]string, mode, conflict string, includeRuns bool, counts *BundleCounts) error {
	hosts := map[string]string{}
	jobs := map[string]string{}
	for _, rec := range data.RemoteHosts {
		h := rec.Host
		if h.ID == "" || h.Name == "" {
			return errors.New("remote host id and name are required")
		}
		signer, err := ssh.ParsePrivateKey([]byte(rec.PrivateKey))
		if err != nil {
			return errors.New("invalid remote SSH private key")
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.PublicKey))
		if err != nil || !bytes.Equal(pub.Marshal(), signer.PublicKey().Marshal()) {
			return errors.New("remote SSH key pair does not match")
		}
		id, exists, err := remoteConflict(ctx, tx, "remote_hosts", h.ID, h.Name)
		if err != nil {
			return err
		}
		hosts[h.ID] = id
		if mode == "merge" && conflict == "skip" && exists {
			continue
		}
		h.ID = id
		h.HealthStatus = "untested"
		h.HealthError = ""
		h.CheckedAt = nil
		h.UpdatedAt = time.Now().UTC()
		encrypted, err := s.cipher.EncryptString(rec.PrivateKey)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(h)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO remote_hosts(id,name,private_key_encrypted,data_json) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,private_key_encrypted=excluded.private_key_encrypted,data_json=excluded.data_json`, h.ID, h.Name, encrypted, string(raw)); err != nil {
			return err
		}
		counts.RemoteHosts++
	}
	for _, j := range data.RemoteJobs {
		if j.ID == "" || j.Name == "" {
			return errors.New("remote job id and name are required")
		}
		id, exists, err := remoteConflict(ctx, tx, "remote_backup_jobs", j.ID, j.Name)
		if err != nil {
			return err
		}
		jobs[j.ID] = id
		if mode == "merge" && conflict == "skip" && exists {
			continue
		}
		hostID, ok := hosts[j.HostID]
		if !ok {
			return errors.New("remote job references missing host")
		}
		j.HostID = hostID
		j.ID = id
		for i, u := range j.UpstreamIDs {
			target, ok := upstreams[u]
			if !ok {
				return errors.New("remote job references missing upstream")
			}
			j.UpstreamIDs[i] = target
		}
		raw, err := json.Marshal(j)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO remote_backup_jobs(id,name,host_id,data_json) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,host_id=excluded.host_id,data_json=excluded.data_json`, j.ID, j.Name, j.HostID, string(raw)); err != nil {
			return err
		}
		counts.RemoteJobs++
	}
	if includeRuns {
		for _, r := range data.RemoteRuns {
			if id, ok := jobs[r.JobID]; ok {
				r.JobID = id
			}
			if id, ok := hosts[r.HostID]; ok {
				r.HostID = id
			}
			for i := range r.Archives {
				for n := range r.Archives[i].Uploads {
					u := &r.Archives[i].Uploads[n]
					if id, ok := upstreams[u.UpstreamID]; ok {
						u.UpstreamID = id
					}
				}
			}
			if r.Status == "running" {
				r.Status = "failed"
				r.Error = "迁移前执行尚未完成，请重新执行任务"
				now := time.Now().UTC()
				r.FinishedAt = &now
			}
			raw, err := json.Marshal(struct {
				Run    db.RemoteRun      `json:"run"`
				Spools map[string]string `json:"spools"`
			}{Run: r, Spools: map[string]string{}})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO remote_backup_runs(id,job_id,status,started_at,data_json) VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, r.ID, r.JobID, r.Status, r.StartedAt, string(raw)); err != nil {
				return err
			}
			counts.RemoteRuns++
		}
	}
	return nil
}
