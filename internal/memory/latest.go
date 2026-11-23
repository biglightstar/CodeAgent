// ===================最近使用的sessionID======================
// LATEST文件
package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// 保存最近使用的会话id
func (s *Store) SetLatestSessionID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("会话 ID 不能为空")
	}
	// 获取要保存的路径
	path := filepath.Join(s.root, "sessions", "LATEST")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// 把会话id值保存到LATEST文件当中
	return os.WriteFile(path, []byte(id), 0o600)
}

// 读取最近使用的会话id
func (s *Store) LatestSessionID() (string, error) {
	// 读取session/LATEST文件中的全部内容
	data, err := os.ReadFile(filepath.Join(s.root, "sessions", "LATEST"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(data)), err
}
