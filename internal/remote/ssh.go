package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/ssh"
	"safekeep/internal/db"
)

const connectTimeout = 20 * time.Second
const maxCommandOutput = 16 * 1024

// 每个会话共享可取消连接，取消任务时也会中止握手及正在执行的远程命令。
type connection struct {
	*ssh.Client
	stop func() bool
}

func (c *connection) Close() error { c.stop(); return c.Client.Close() }

func generateKey() (string, string, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(key, "safekeep remote backup")
	if err != nil {
		return "", "", err
	}
	public, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))), nil
}

func (s *Service) connect(ctx context.Context, h *db.RemoteHost, enroll bool) (*connection, string, error) {
	plain, err := s.cipher.DecryptString(h.PrivateKeyEncrypted)
	if err != nil {
		return nil, "", errors.New("无法读取 SSH 私钥")
	}
	keyBytes := []byte(plain)
	signer, err := ssh.ParsePrivateKey(keyBytes)
	clear(keyBytes)
	if err != nil {
		return nil, "", errors.New("SSH 私钥格式无效")
	}
	observed := ""
	cfg := &ssh.ClientConfig{User: h.Username, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: connectTimeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			observed = string(ssh.MarshalAuthorizedKey(key))
			if h.HostKey != "" {
				expected, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.HostKey))
				if err != nil || !bytes.Equal(expected.Marshal(), key.Marshal()) {
					return errors.New("SSH 主机密钥发生变化，请核实远程主机身份")
				}
			} else if !enroll {
				return errors.New("请先在主机页面测试连接并登记 SSH 主机指纹")
			}
			return nil
		}}
	address := net.JoinHostPort(h.Address, strconv.Itoa(h.Port))
	dialer := net.Dialer{Timeout: connectTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, "", fmt.Errorf("SSH 连接失败: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	deadline := time.Now().Add(connectTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		stop()
		conn.Close()
		return nil, "", err
	}
	cc, ch, reqs, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		stop()
		conn.Close()
		return nil, "", fmt.Errorf("SSH 握手或公钥认证失败: %w", err)
	}
	deadline = time.Time{}
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		stop()
		cc.Close()
		return nil, "", err
	}
	return &connection{ssh.NewClient(cc, ch, reqs), stop}, observed, nil
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < maxCommandOutput {
		keep := min(n, maxCommandOutput-b.Len())
		b.Buffer.Write(p[:keep])
	}
	return n, nil
}

func runCommand(c *connection, command string, out io.Writer) error {
	session, err := c.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	var stderr cappedBuffer
	session.Stderr = &stderr
	session.Stdout = out
	if err := session.Run(command); err != nil {
		return fmt.Errorf("远程命令执行失败: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func shellQuote(v string) string      { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
func powershellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
func powershell(script string) string {
	chars := utf16.Encode([]rune(script))
	raw := make([]byte, len(chars)*2)
	for i, c := range chars {
		binary.LittleEndian.PutUint16(raw[i*2:], c)
	}
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(raw)
}

// 通过实际创建空归档检测能力，避免只有 tar 命令但缺少压缩支持时误报健康。
func detectPlatform(c *connection) (string, string, error) {
	var output cappedBuffer
	if err := runCommand(c, "uname -s", &output); err == nil {
		err = runCommand(c, "tar -czf /dev/null --exclude='safekeep-capability-probe' -T /dev/null", io.Discard)
		if err != nil {
			return strings.TrimSpace(output.String()), "", errors.New("缺少可用的 tar.gz 打包工具或不支持 --exclude，请安装支持 gzip 压缩和排除规则的 tar")
		}
		return "posix", "tar", nil
	}
	script := `$ErrorActionPreference='Stop'; $d=Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString()); $a=$d+'.tar.gz'; try { New-Item -ItemType Directory -Path $d | Out-Null; & tar.exe -czf $a --exclude=safekeep-capability-probe -C $d .; if ($LASTEXITCODE -ne 0) { throw 'tar.gz unavailable' }; [Console]::Out.Write('windows') } finally { Remove-Item -LiteralPath $d -Recurse -Force -ErrorAction SilentlyContinue; Remove-Item -LiteralPath $a -Force -ErrorAction SilentlyContinue }`
	output.Reset()
	if err := runCommand(c, powershell(script), &output); err == nil && strings.TrimSpace(output.String()) == "windows" {
		return "windows", "tar.exe", nil
	}
	return "unknown", "", errors.New("未检测到受支持的打包环境；需要 POSIX shell 与 tar（支持 gzip/--exclude），或 Windows PowerShell 与 tar.exe")
}

func archiveCommand(platform string, dir db.RemoteDirectory) string {
	if platform == "windows" {
		args := []string{"'-czf'", "$a"}
		for _, pattern := range dir.Excludes {
			args = append(args, powershellQuote("--exclude="+pattern))
		}
		args = append(args, "'-C'", powershellQuote(dir.Path), "'.'")
		// 使用标准输出的字节流传输，避免 PowerShell 文本管道损坏归档内容。
		return powershell(`$ErrorActionPreference='Stop'; $a=[IO.Path]::GetTempFileName(); try { & tar.exe ` + strings.Join(args, " ") + `; if ($LASTEXITCODE -ne 0) { throw 'tar failed' }; $f=[IO.File]::OpenRead($a); try { $f.CopyTo([Console]::OpenStandardOutput()) } finally { $f.Dispose() } } finally { Remove-Item -LiteralPath $a -Force -ErrorAction SilentlyContinue }`)
	}
	args := []string{"tar", "-czf", "-"}
	for _, pattern := range dir.Excludes {
		args = append(args, shellQuote("--exclude="+pattern))
	}
	args = append(args, ".")
	return "cd " + shellQuote(dir.Path) + " && " + strings.Join(args, " ")
}
