// Package remote 实现迅雷云端的设备与下载任务管理。
package remote

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"
)

type state struct {
	DeviceID            string                `json:"device_id"`
	Username            string                `json:"username,omitempty"`
	Password            string                `json:"password,omitempty"`
	Remember            bool                  `json:"remember_password,omitempty"`
	AccessToken         string                `json:"access_token,omitempty"`
	RefreshToken        string                `json:"refresh_token,omitempty"`
	CaptchaToken        string                `json:"captcha_token,omitempty"`
	UserID              string                `json:"user_id,omitempty"`
	ExpiresAt           int64                 `json:"expires_at,omitempty"`
	Target              string                `json:"target,omitempty"`
	TargetName          string                `json:"target_name,omitempty"`
	Directory           string                `json:"directory,omitempty"`
	CreditKey           string                `json:"credit_key,omitempty"`
	ReviewURL           string                `json:"review_url,omitempty"`
	ReviewKey           string                `json:"review_key,omitempty"`
	ReviewToken         string                `json:"review_token,omitempty"`
	ReviewVerified      bool                  `json:"review_verified,omitempty"`
	ReviewSecurityToken string                `json:"review_security_token,omitempty"`
	ReviewReported      bool                  `json:"review_reported,omitempty"`
	ReviewSession       string                `json:"review_session,omitempty"`
	ReviewCookies       map[string]string     `json:"review_cookies,omitempty"`
	ReviewSentAt        int64                 `json:"review_sent_at,omitempty"`
	VerificationURL     string                `json:"verification_url,omitempty"`
	Submissions         map[string]submission `json:"submissions,omitempty"`
}

func (s *state) clearReview() {
	s.ReviewURL, s.ReviewKey, s.ReviewToken = "", "", ""
	s.ReviewVerified, s.ReviewReported = false, false
	s.ReviewSecurityToken, s.ReviewSession = "", ""
	s.ReviewCookies = nil
	s.ReviewSentAt = 0
}

type submission struct {
	Fingerprint string `json:"fingerprint"`
	Target      string `json:"target"`
	URL         string `json:"url"`
	Directory   string `json:"directory"`
	TaskID      string `json:"task_id,omitempty"`
	At          int64  `json:"at"`
}

// Status 仅包含允许发给管理页面的状态，不返回任何认证凭据。
type Status struct {
	LoggedIn        bool   `json:"logged_in"`
	Username        string `json:"username"`
	Remember        bool   `json:"remember_password"`
	Target          string `json:"target"`
	TargetName      string `json:"target_name"`
	Directory       string `json:"directory"`
	ReviewRequired  bool   `json:"review_required"`
	Mobile          string `json:"mobile"`
	VerificationURL string `json:"verification_url,omitempty"`
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func readState(path string) (state, error) {
	s := state{DeviceID: randomID()}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("读取远程登录状态失败：%w", err)
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, errors.New("远程登录状态文件损坏，请先备份并移走该文件后重启")
	}
	if len(s.DeviceID) != 32 {
		s.DeviceID = randomID()
	}
	if !s.Remember {
		s.Password = ""
	}
	return s, nil
}

// 文件操作按客户端隔离，便于在临时目录中验证各保存步骤失败后的状态。
type stateFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
	Name() string
}

type stateFiles struct {
	mkdirAll   func(string, os.FileMode) error
	chmod      func(string, os.FileMode) error
	createTemp func(string, string) (stateFile, error)
	rename     func(string, string) error
	open       func(string) (stateFile, error)
}

func localStateFiles() stateFiles {
	return stateFiles{
		mkdirAll: os.MkdirAll, chmod: os.Chmod, rename: os.Rename,
		createTemp: func(dir, pattern string) (stateFile, error) { return os.CreateTemp(dir, pattern) },
		open:       func(path string) (stateFile, error) { return os.Open(path) },
	}
}

// save 在调用者持有操作锁时执行，临时文件从创建起即只允许当前用户读写。
func (c *Client) save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.writeState(c.state)
	return err
}

// updateAndSave 仅用于尚未发出上游写请求的本地修改，文件替换后才发布新状态。
// 替换后同步失败仍采用已写入的状态并返回错误，避免内存与文件内容不一致。
// 已取得的远端结果仍使用 update 和 save，保存失败时保留内存中的进度。
func (c *Client) updateAndSave(fn func(*state) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.state
	s.ReviewCookies = maps.Clone(s.ReviewCookies)
	s.Submissions = maps.Clone(s.Submissions)
	if err := fn(&s); err != nil {
		return err
	}
	replaced, err := c.writeState(s)
	if replaced {
		c.state = s
	}
	return err
}

// writeState 在调用者持有状态写锁时保存指定副本，不改变当前内存状态。
// 返回值区分文件是否已替换；成功还要求同步状态目录及祖先目录。
func (c *Client) writeState(s state) (replaced bool, err error) {
	if !s.Remember {
		s.Password = ""
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return false, errors.New("编码远程登录状态失败")
	}
	dir, err := filepath.Abs(filepath.Dir(c.path))
	if err != nil {
		return false, errors.New("无法定位远程状态目录")
	}
	if err = c.files.mkdirAll(dir, 0700); err != nil {
		return false, errors.New("无法创建远程状态目录，请检查数据目录权限")
	}
	if err = c.files.chmod(dir, 0700); err != nil {
		return false, errors.New("无法设置远程状态目录权限")
	}
	f, err := c.files.createTemp(dir, ".state-*")
	if err != nil {
		return false, errors.New("无法写入远程登录状态，请检查数据目录权限")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	if err == nil {
		err = c.files.rename(f.Name(), c.path)
	}
	if err != nil {
		return false, errors.New("保存远程登录状态失败，请检查数据目录权限和剩余空间")
	}
	// 同步整个目录链，覆盖本次新建的多级目录，以及上次失败后留下的目录。
	// 不能仅在首次创建目录时同步父目录，否则失败后的重试可能遗漏目录项。
	for {
		if err := c.syncStateDir(dir); err != nil {
			return true, errors.New("远程登录状态已替换，但同步状态目录失败，请检查文件系统后重试")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return true, nil
		}
		dir = parent
	}
}

func (c *Client) syncStateDir(dir string) error {
	f, err := c.files.open(dir)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (c *Client) snapshot() state {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *Client) update(fn func(*state)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(&c.state)
}

func (c *Client) Status() Status {
	s := c.snapshot()
	return Status{LoggedIn: s.AccessToken != "" || s.RefreshToken != "", Username: s.Username,
		Remember: s.Remember, Target: s.Target, TargetName: s.TargetName, Directory: s.Directory,
		ReviewRequired: s.ReviewURL != "", Mobile: maskedAccount(s.Username), VerificationURL: s.VerificationURL}
}

func (c *Client) pruneSubmissions(s *state) {
	for id, v := range s.Submissions {
		if time.Since(time.Unix(v.At, 0)) > 7*24*time.Hour {
			delete(s.Submissions, id)
		}
	}
}
