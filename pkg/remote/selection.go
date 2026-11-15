package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

func normalizeSelection(mode string, values []int64) ([]int64, error) {
	if len(values) > maxResourceFiles {
		return nil, failure("invalid_selection", "所选文件数量超过上限")
	}
	if mode == "single" && len(values) != 0 || mode != "files" && mode != "single" && (mode != "" || values != nil) || mode == "files" && len(values) == 0 {
		return nil, failure("invalid_selection", "请明确选择至少一个有效文件")
	}
	set := map[int64]bool{}
	for _, n := range values {
		if n < 0 || n > maxFileIndex {
			return nil, failure("invalid_selection", "文件索引无效")
		}
		set[n] = true
	}
	out := []int64{}
	for n := range set {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func encodeIndices(values []int64) string {
	parts := []string{}
	for i := 0; i < len(values); i++ {
		start, end := values[i], values[i]
		for i+1 < len(values) && values[i+1] == end+1 {
			i++
			end = values[i]
		}
		s := strconv.FormatInt(start, 10)
		if end != start {
			s += "-" + strconv.FormatInt(end, 10)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}

func selectionFingerprint(target, directory, link, mode, version string, indices []int64) string {
	b, _ := json.Marshal(struct {
		Version                                       int
		Target, Directory, URL, Mode, ResourceVersion string
		Indices                                       []int64
	}{2, target, directory, link, mode, version, indices})
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}

func (c *Client) validatePreview(req CreateRequest, link string) (*resourceSnapshot, error) {
	p := c.previews[req.PreviewID]
	if p == nil || !time.Now().Before(p.preview.ExpiresAt) {
		return nil, failure("preview_expired", "资源预览已过期，请重新解析并确认文件")
	}
	s := c.snapshot()
	if p.account != s.Username || p.userID != s.UserID || p.session != c.resourceSession || p.preview.Target != req.Target || p.preview.URL != link || p.preview.ResourceVersion != req.ResourceVersion || p.preview.Kind != req.SelectionMode {
		return nil, failure("preview_mismatch", "预览与当前账号、设备、链接或文件内容不一致，请重新解析")
	}
	return p, nil
}

// selectionEvidence 仅在所有区间都属于原集合时确认；不展开上游的任意大区间。
func selectionEvidence(s submission, encoded string) bool {
	if s.Version == 0 {
		return !isMagnetLink(s.URL)
	}
	if s.Version != 2 {
		return false
	}
	if s.SelectionMode == "single" {
		return !isMagnetLink(s.URL)
	}
	if s.SelectionMode != "files" || encoded == "" || len(encoded) > 128<<10 || len(s.FileIndices) == 0 {
		return false
	}
	parts := strings.Split(encoded, ",")
	if len(parts) > maxResourceFiles {
		return false
	}
	selected := map[int64]bool{}
	for _, n := range s.FileIndices {
		selected[n] = true
	}
	covered := map[int64]bool{}
	for _, part := range parts {
		bounds := strings.Split(strings.TrimSpace(part), "-")
		if len(bounds) > 2 || len(bounds) == 0 {
			return false
		}
		start, err := strconv.ParseInt(bounds[0], 10, 64)
		if err != nil || start < 0 || start > maxFileIndex {
			return false
		}
		end := start
		if len(bounds) == 2 {
			end, err = strconv.ParseInt(bounds[1], 10, 64)
		}
		if err != nil || end < start || end > maxFileIndex || end-start >= int64(len(selected)) {
			return false
		}
		for n := start; n <= end; n++ {
			if !selected[n] {
				return false
			}
			covered[n] = true
		}
	}
	return len(covered) == len(selected)
}

type SubmissionResult struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	TaskID    string `json:"task_id,omitempty"`
}

// CheckSubmission 永远只核对已有记录，即使预览丢失或记录过期也不创建任务。
func (c *Client) CheckSubmission(ctx context.Context, target, id string) (SubmissionResult, error) {
	if err := c.lock(ctx); err != nil {
		return SubmissionResult{}, err
	}
	defer c.unlock()
	if err := c.checkTarget(target); err != nil {
		return SubmissionResult{}, err
	}
	if len(id) < 16 || len(id) > 80 {
		return SubmissionResult{}, failure("invalid_request", "任务请求标识无效")
	}
	out := SubmissionResult{RequestID: id, Status: "missing"}
	s, ok := c.snapshot().Submissions[id]
	if !ok {
		return out, nil
	}
	if s.Version != 0 && s.Version != 2 {
		return out, failure("invalid_request", "提交记录版本不受支持，请保留记录并升级程序")
	}
	if s.Target != target || s.Version == 2 && s.Account != c.snapshot().Username {
		return out, failure("settings_changed", "提交记录与当前账号或设备不一致")
	}
	out.Status = "unknown"
	idTask := s.TaskID
	if idTask == "" {
		idTask = c.reconcile(ctx, s)
	}
	if idTask != "" {
		if s.TaskID == "" {
			c.update(func(st *state) { v := st.Submissions[id]; v.TaskID = idTask; st.Submissions[id] = v })
			if err := c.save(); err != nil {
				return out, failure("submission_unknown", "已找到远端任务，但保存核对结果失败，请再次检查")
			}
		}
		out.Status = "confirmed"
		out.TaskID = idTask
	}
	return out, nil
}
