package remote

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
)

// EncodePath 分隔符使用感叹号，原有感叹号和波浪号使用独立前缀转义。
// Windows 的反斜杠和冒号分别使用 ~^ 和 ~;，保留原路径信息。
func EncodePath(source string) string {
	var b strings.Builder
	for _, r := range source {
		switch r {
		case '/':
			b.WriteByte('!')
		case '!':
			b.WriteString("~!")
		case '~':
			b.WriteString("~~")
		case '\\':
			b.WriteString("~^")
		case ':':
			b.WriteString("~;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func DecodePath(encoded string) (string, error) {
	var b strings.Builder
	rs := []rune(encoded)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '!' {
			b.WriteRune('/')
			continue
		}
		if r != '~' {
			b.WriteRune(r)
			continue
		}
		i++
		if i >= len(rs) {
			return "", errors.New("无效的路径转义")
		}
		switch rs[i] {
		case '!', '~':
			b.WriteRune(rs[i])
		case '^':
			b.WriteRune('\\')
		case ';':
			b.WriteRune(':')
		default:
			return "", errors.New("无效的路径转义")
		}
	}
	return b.String(), nil
}

func validateDirectory(source string) error {
	if source == "" || strings.ContainsFunc(source, unicode.IsControl) {
		return errors.New("源目录不能为空或包含控制字符")
	}
	windows := len(source) >= 3 && ((source[0] >= 'A' && source[0] <= 'Z') || (source[0] >= 'a' && source[0] <= 'z')) && source[1] == ':' && (source[2] == '\\' || source[2] == '/')
	if !strings.HasPrefix(source, "/") && !windows && !strings.HasPrefix(source, `\\`) {
		return errors.New("源目录必须填写完整的绝对路径")
	}
	if len(EncodePath(source)) > 180 {
		return fmt.Errorf("源目录编码后超过 180 字节，请使用更短的绝对路径")
	}
	return nil
}

func validateParent(parent string) error {
	if parent == "" {
		return errors.New("上游父目录不能为空")
	}
	if strings.HasPrefix(parent, "/") || strings.ContainsAny(parent, "\\\x00\r\n") {
		return errors.New("上游父目录必须为相对路径")
	}
	for _, p := range strings.Split(parent, "/") {
		if p == ".." || p == "." || p == "" {
			return errors.New("上游父目录不能包含空路径、. 或 ..")
		}
	}
	if path.Clean(parent) != parent {
		return errors.New("上游父目录格式无效")
	}
	return nil
}
