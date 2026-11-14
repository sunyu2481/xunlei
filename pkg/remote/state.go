// Package remote 实现迅雷云端的设备与下载任务管理。
package remote

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// save 在调用者持有操作锁时执行，临时文件从创建起即只允许当前用户读写。
func (c *Client) save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.state
	if !s.Remember {
		s.Password = ""
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return errors.New("编码远程登录状态失败")
	}
	dir := filepath.Dir(c.path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return errors.New("无法创建远程状态目录，请检查数据目录权限")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return errors.New("无法设置远程状态目录权限")
	}
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return errors.New("无法写入远程登录状态，请检查数据目录权限")
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
		err = os.Rename(f.Name(), c.path)
	}
	if err != nil {
		return errors.New("保存远程登录状态失败，请检查数据目录权限和剩余空间")
	}
	return nil
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
