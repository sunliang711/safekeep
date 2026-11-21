package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"safekeep/internal/db"
)

func verifySpool(name string, a *db.RemoteArchive) error {
	f, err := os.Open(name)
	if err != nil {
		return errors.New("本地归档已清理，请重新执行备份任务")
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if size != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("本地归档校验失败，请重新执行备份任务")
	}
	return nil
}
