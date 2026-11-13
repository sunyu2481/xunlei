package remote

import (
	"context"
	"errors"
	"math"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Device struct {
	Target string `json:"target"`
	Name   string `json:"name"`
}
type Folder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}
type Task struct {
	ID        string  `json:"id"`
	Type      string  `json:"type"`
	Name      string  `json:"name"`
	Phase     string  `json:"phase"`
	Progress  float64 `json:"progress"`
	Size      int64   `json:"size"`
	Done      int64   `json:"done"`
	Speed     int64   `json:"speed"`
	Directory string  `json:"directory"`
	URL       string  `json:"url"`
	createdAt int64
}
type TaskPage struct {
	Tasks         []Task `json:"tasks"`
	NextPageToken string `json:"next_page_token"`
}
type Settings struct {
	Target    string `json:"target"`
	Directory string `json:"directory"`
}
type CreateRequest struct {
	RequestID string `json:"request_id"`
	Target    string `json:"target"`
	Directory string `json:"directory"`
	URL       string `json:"url"`
}
type CreateResult struct {
	ID     string `json:"id"`
	Reused bool   `json:"reused"`
}

func (c *Client) devices(ctx context.Context) ([]Device, error) {
	out := []Device{}
	seen := map[string]bool{}
	page := ""
	for i := 0; i < 20; i++ {
		d, err := c.request(ctx, "GET", driveURL+"/tasks", url.Values{"type": {"user#runner"}, "limit": {"200"}, "page_token": {page}}, nil)
		if err != nil {
			return nil, err
		}
		for _, entry := range items(d["tasks"]) {
			t := obj(entry)
			target := str(obj(t["params"])["target"])
			if target != "" && !seen[target] {
				out = append(out, Device{Target: target, Name: first(str(t["name"]), target)})
				seen[target] = true
			}
		}
		next := first(str(d["next_page_token"]), str(d["nextPageToken"]))
		if next == "" {
			return out, nil
		}
		if next == page {
			break
		}
		page = next
	}
	return nil, failure("upstream", "设备列表分页异常，请稍后重试")
}

func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	return c.devices(ctx)
}

func (c *Client) checkTarget(target string) error {
	if target == "" {
		return failure("device_required", "请先选择远程下载设备")
	}
	if target != c.snapshot().Target {
		return failure("settings_changed", "目标设备已发生变化，请刷新页面后重试")
	}
	return nil
}

func (c *Client) discover(ctx context.Context, target string, refresh bool) (relay, error) {
	if r, ok := c.relays[target]; ok && !refresh && time.Since(r.At) < 5*time.Minute {
		return r, nil
	}
	d, err := c.request(ctx, "GET", driveURL+"/apps/INNER_API", url.Values{"space": {target}}, nil)
	if err != nil {
		return relay{}, err
	}
	link := str(d["link"])
	for _, v := range items(d["links"]) {
		if link == "" {
			link = str(obj(v)["url"])
		}
	}
	if !trustedURL(link) {
		return relay{}, failure("device_unavailable", "设备未返回可用的目录服务，请检查迅雷远程插件")
	}
	u, _ := url.Parse(link)
	q := url.Values{"space": {target}, "device_space": {""}}
	for _, key := range []string{"plugin_app_id", "plugin_app_token"} {
		if v := u.Query().Get(key); v != "" {
			q.Set(key, v)
		}
	}
	u.RawQuery = ""
	u.Fragment = ""
	r := relay{Base: strings.TrimRight(u.String(), "/") + "/", Query: q, At: time.Now()}
	c.relays[target] = r
	return r, nil
}

func cloneQuery(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func folderPath(d object, parent string) string {
	p := obj(d["params"])
	for _, key := range []string{"RealPath", "realPath", "real_path", "AliasPath", "aliasPath", "path"} {
		if s := str(p[key]); s != "" {
			return path.Clean("/" + strings.TrimPrefix(s, "/"))
		}
	}
	name := str(d["name"])
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return ""
	}
	return path.Join("/", parent, name)
}

func (c *Client) folders(ctx context.Context, target, parentID, parentPath string) ([]Folder, error) {
	var last error
	for round := 0; round < 2; round++ {
		r, err := c.discover(ctx, target, round > 0)
		if err != nil {
			return nil, err
		}
		out := []Folder{}
		seen := map[string]bool{}
		page := ""
		for i := 0; i < 50; i++ {
			q := cloneQuery(r.Query)
			q.Set("parent_id", parentID)
			q.Set("limit", "200")
			q.Set("page_token", page)
			q.Set("filters", `{"kind":{"eq":"drive#folder"}}`)
			q["with"] = []string{"withCategoryDiskMountPath", "withCategoryHistoryDownloadPath"}
			d, e := c.request(ctx, "GET", r.Base+"drive/v1/files", q, nil)
			if e != nil {
				last = e
				break
			}
			for _, v := range items(d["files"]) {
				f := obj(v)
				id := str(f["id"])
				kind := str(f["kind"])
				if id == "" || seen[id] || (kind != "" && kind != "drive#folder") {
					continue
				}
				fp := folderPath(f, parentPath)
				if fp == "" {
					continue
				}
				out = append(out, Folder{ID: id, Name: first(str(f["name"]), fp), Path: fp})
				seen[id] = true
			}
			next := first(str(d["next_page_token"]), str(d["nextPageToken"]))
			if next == "" {
				sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
				return out, nil
			}
			if next == page {
				return nil, failure("upstream", "目录分页异常，请稍后重试")
			}
			page = next
			if i == 49 {
				return nil, failure("upstream", "此目录包含过多子目录，请缩小选择范围")
			}
		}
		var e *Error
		if !errors.As(last, &e) || (e.Kind != "device_unavailable" && e.Kind != "upstream") {
			return nil, last
		}
	}
	return nil, last
}

func cleanDirectory(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if len(p) > 4096 || !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\\") {
		return "", failure("invalid_request", "请使用设备目录选择器选择下载目录")
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." {
			return "", failure("invalid_request", "下载目录不能包含上级路径")
		}
	}
	return path.Clean(p), nil
}

func (c *Client) resolve(ctx context.Context, target, directory string) (Folder, error) {
	directory, err := cleanDirectory(directory)
	if err != nil {
		return Folder{}, err
	}
	roots, err := c.folders(ctx, target, "", "")
	if err != nil {
		return Folder{}, err
	}
	if directory == "" && len(roots) == 1 {
		return roots[0], nil
	}
	var current Folder
	for _, f := range roots {
		if directory == f.Path || strings.HasPrefix(directory, strings.TrimRight(f.Path, "/")+"/") {
			if len(f.Path) > len(current.Path) {
				current = f
			}
		}
	}
	if current.ID == "" {
		return Folder{}, failure("directory_required", "请从设备目录中选择有效的下载位置")
	}
	for depth := 0; depth < 32; depth++ {
		if current.Path == directory {
			return current, nil
		}
		children, e := c.folders(ctx, target, current.ID, current.Path)
		if e != nil {
			return Folder{}, e
		}
		var next Folder
		for _, f := range children {
			if (directory == f.Path || strings.HasPrefix(directory, strings.TrimRight(f.Path, "/")+"/")) && len(f.Path) > len(current.Path) && len(f.Path) > len(next.Path) {
				next = f
			}
		}
		if next.ID == "" {
			return Folder{}, failure("directory_required", "所选目录不存在或设备无权访问，请重新选择")
		}
		current = next
	}
	return Folder{}, failure("invalid_request", "下载目录层级过深")
}

// Folders 只读浏览。目录不存在时不会自动创建，也不会退回其他下载位置。
func (c *Client) Folders(ctx context.Context, target, directory string) ([]Folder, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	if err := c.checkTarget(target); err != nil {
		return nil, err
	}
	if directory == "" {
		return c.folders(ctx, target, "", "")
	}
	f, err := c.resolve(ctx, target, directory)
	if err != nil {
		return nil, err
	}
	return c.folders(ctx, target, f.ID, f.Path)
}

func (c *Client) Configure(ctx context.Context, settings Settings) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	devices, err := c.devices(ctx)
	if err != nil {
		return err
	}
	var name string
	for _, d := range devices {
		if d.Target == settings.Target {
			name = d.Name
			break
		}
	}
	if name == "" {
		return failure("device_required", "未找到所选设备，请确认设备已绑定到当前迅雷账号")
	}
	directory := ""
	if settings.Directory != "" {
		f, e := c.resolve(ctx, settings.Target, settings.Directory)
		if e != nil {
			return e
		}
		directory = f.Path
	}
	c.update(func(s *state) { s.Target = settings.Target; s.TargetName = name; s.Directory = directory })
	return c.save()
}

func (c *Client) taskPage(ctx context.Context, target, page string) (TaskPage, error) {
	d, err := c.request(ctx, "GET", driveURL+"/tasks", url.Values{"space": {target}, "limit": {"200"}, "page_token": {page},
		"filters": {`{"type":{"in":"user#download,user#download-url"}}`}}, nil)
	if err != nil {
		return TaskPage{}, err
	}
	out := TaskPage{Tasks: []Task{}, NextPageToken: first(str(d["next_page_token"]), str(d["nextPageToken"]))}
	for _, v := range items(d["tasks"]) {
		t := obj(v)
		p := obj(t["params"])
		kind := str(t["type"])
		if kind != "user#download" && kind != "user#download-url" {
			continue
		}
		if space := str(t["space"]); space != "" && space != target {
			continue
		}
		size, done := integer(t["file_size"]), integer(t["status_size"])
		progress := float64(0)
		if size > 0 {
			progress = 100 * float64(done) / float64(size)
		} else if value, e := strconv.ParseFloat(str(t["progress"]), 64); e == nil {
			progress = value
			if progress > 100 {
				progress /= 100
			}
		}
		if str(t["phase"]) == "PHASE_TYPE_COMPLETE" {
			progress = 100
		}
		if math.IsNaN(progress) || math.IsInf(progress, 0) {
			progress = 0
		}
		createdAt := integer(t["created_time"])
		if createdAt > 1e12 {
			createdAt /= 1000
		}
		if createdAt == 0 {
			if created, e := time.Parse(time.RFC3339Nano, str(t["created_time"])); e == nil {
				createdAt = created.Unix()
			}
		}
		out.Tasks = append(out.Tasks, Task{ID: str(t["id"]), Type: kind, Name: first(str(t["name"]), str(t["file_name"]), "未命名任务"),
			Phase: str(t["phase"]), Progress: min(100, max(0, progress)), Size: size, Done: done, Speed: integer(t["download_speed"]), Directory: str(p["parent_folder_path"]), URL: str(p["url"]), createdAt: createdAt})
	}
	return out, nil
}

func (c *Client) Tasks(ctx context.Context, target, page string) (TaskPage, error) {
	if err := c.lock(ctx); err != nil {
		return TaskPage{}, err
	}
	defer c.unlock()
	if err := c.checkTarget(target); err != nil {
		return TaskPage{}, err
	}
	if len(page) > 4096 {
		return TaskPage{}, failure("invalid_request", "分页参数无效")
	}
	return c.taskPage(ctx, target, page)
}

func (c *Client) findTask(ctx context.Context, target, id string) (Task, error) {
	page := ""
	for i := 0; i < 50; i++ {
		p, err := c.taskPage(ctx, target, page)
		if err != nil {
			return Task{}, err
		}
		for _, t := range p.Tasks {
			if t.ID == id {
				return t, nil
			}
		}
		if p.NextPageToken == "" || p.NextPageToken == page {
			break
		}
		page = p.NextPageToken
	}
	return Task{}, failure("not_found", "当前设备中未找到该任务，请刷新任务列表")
}

func (c *Client) TaskAction(ctx context.Context, target, id, action string) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	if err := c.checkTarget(target); err != nil {
		return err
	}
	if action != "pause" && action != "resume" && action != "delete" {
		return failure("invalid_request", "不支持的任务操作")
	}
	t, err := c.findTask(ctx, target, id)
	if err != nil {
		return err
	}
	if action == "delete" {
		_, err = c.request(ctx, "DELETE", driveURL+"/tasks", url.Values{"space": {target}, "task_ids": {id}}, nil)
	} else {
		phase := "pause"
		if action == "resume" {
			phase = "running"
		}
		_, err = c.request(ctx, "PATCH", driveURL+"/task", nil, object{"space": target, "type": t.Type, "id": t.ID, "set_params": object{"spec": `{"phase":"` + phase + `"}`}})
	}
	return err
}

func (c *Client) reconcile(ctx context.Context, s submission) string {
	page := ""
	matched := ""
	for i := 0; i < 5; i++ {
		p, err := c.taskPage(ctx, s.Target, page)
		if err != nil {
			return ""
		}
		for _, t := range p.Tasks {
			if t.URL == s.URL && path.Clean(t.Directory) == path.Clean(s.Directory) && t.createdAt >= s.At-5 && t.createdAt <= time.Now().Unix()+60 {
				if matched != "" && matched != t.ID {
					return ""
				}
				matched = t.ID
			}
		}
		if p.NextPageToken == "" || p.NextPageToken == page {
			break
		}
		page = p.NextPageToken
	}
	return matched
}

func (c *Client) Create(ctx context.Context, req CreateRequest) (CreateResult, error) {
	if err := c.lock(ctx); err != nil {
		return CreateResult{}, err
	}
	defer c.unlock()
	if err := c.checkTarget(req.Target); err != nil {
		return CreateResult{}, err
	}
	if len(req.RequestID) < 16 || len(req.RequestID) > 80 {
		return CreateResult{}, failure("invalid_request", "任务请求标识无效，请刷新页面")
	}
	link, err := NormalizeLink(req.URL)
	if err != nil {
		return CreateResult{}, err
	}
	directory, err := cleanDirectory(req.Directory)
	if err != nil {
		return CreateResult{}, err
	}
	fingerprint := md5hex(req.Target + "\n" + directory + "\n" + link)
	if old, ok := c.snapshot().Submissions[req.RequestID]; ok {
		if old.Fingerprint != fingerprint {
			return CreateResult{}, failure("invalid_request", "重复请求的内容发生变化，请重新新建任务")
		}
		if old.TaskID != "" {
			return CreateResult{ID: old.TaskID, Reused: true}, nil
		}
		if id := c.reconcile(ctx, old); id != "" {
			c.update(func(s *state) { v := s.Submissions[req.RequestID]; v.TaskID = id; s.Submissions[req.RequestID] = v })
			if err = c.save(); err != nil {
				return CreateResult{}, err
			}
			return CreateResult{ID: id, Reused: true}, nil
		}
		return CreateResult{}, failure("submission_unknown", "此前提交结果仍未确认，请在任务列表或迅雷客户端核对；本次未重复提交")
	}
	folder, err := c.resolve(ctx, req.Target, directory)
	if err != nil {
		return CreateResult{}, err
	}
	d, err := c.request(ctx, "POST", driveURL+"/resource/list", nil, object{"page_size": 2000, "urls": link})
	if err != nil {
		return CreateResult{}, err
	}
	resources := items(obj(d["list"])["resources"])
	if len(resources) == 0 {
		return CreateResult{}, failure("invalid_request", "迅雷未能解析此链接，请检查链接内容")
	}
	r := obj(resources[0])
	name := first(str(r["name"]), str(r["file_name"]), "新建下载")
	count := first(str(r["file_count"]), "1")
	c.update(func(s *state) {
		c.pruneSubmissions(s)
		if s.Submissions == nil {
			s.Submissions = map[string]submission{}
		}
	})
	if len(c.snapshot().Submissions) >= 2048 {
		return CreateResult{}, failure("rate_limited", "近期提交记录过多，请稍后再试")
	}
	// 在发送前落盘，进程退出或响应丢失后仍能识别同一次提交。
	c.update(func(s *state) {
		s.Submissions[req.RequestID] = submission{Fingerprint: fingerprint, Target: req.Target, URL: link, Directory: folder.Path, At: time.Now().Unix()}
	})
	if err = c.save(); err != nil {
		return CreateResult{}, err
	}
	body := object{"type": "user#download-url", "name": name, "file_name": name, "file_size": first(str(r["file_size"]), "0"), "space": req.Target,
		"params": object{"target": req.Target, "url": link, "total_file_count": count, "parent_folder_path": strings.TrimRight(folder.Path, "/") + "/", "parent_folder_id": folder.ID,
			"sub_file_index": "--1,", "mime_type": str(r["mime_type"]), "file_id": str(r["file_id"])}}
	d, err = c.request(ctx, "POST", driveURL+"/task", nil, body)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Kind != "network" && e.Kind != "device_unavailable" && (e.Kind != "upstream" || e.RemoteCode != 0) {
			c.update(func(s *state) { delete(s.Submissions, req.RequestID) })
			if saveErr := c.save(); saveErr != nil {
				return CreateResult{}, saveErr
			}
			return CreateResult{}, err
		}
		return CreateResult{}, failure("submission_unknown", "提交结果暂未确认，请刷新任务列表核对；再次检查此条结果不会重复创建任务")
	}
	id := first(str(obj(d["task"])["id"]), str(d["id"]))
	if id == "" {
		return CreateResult{}, failure("submission_unknown", "迅雷未返回任务编号，请在任务列表核对提交结果")
	}
	c.update(func(s *state) { v := s.Submissions[req.RequestID]; v.TaskID = id; s.Submissions[req.RequestID] = v })
	if err = c.save(); err != nil {
		return CreateResult{}, failure("submission_unknown", "任务已提交，但保存提交记录失败，请在任务列表核对")
	}
	return CreateResult{ID: id}, nil
}
