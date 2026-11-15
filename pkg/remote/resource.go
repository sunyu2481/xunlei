package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxResourceFiles           = 5000
	maxResourceNodes           = 10000
	maxResourceDepth           = 64
	maxResourceRequests        = 64
	maxResourceBytes           = 16 << 20
	maxPreviewBytes            = 4 << 20
	maxPreviews                = 64
	maxPreviewCacheBytes       = 64 << 20
	maxFileIndex         int64 = 1<<53 - 1
	previewLifetime            = 10 * time.Minute
)

type ResolveRequest struct {
	Target string `json:"target"`
	URL    string `json:"url"`
}

type ResourceFile struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
	IsDir    bool   `json:"is_dir"`
	Index    *int64 `json:"index,omitempty"`
	Size     string `json:"size"`
}

// ResourcePreview 只包含完整且经过校验的资源，不暴露上游列表或认证凭据。
type ResourcePreview struct {
	PreviewID       string         `json:"preview_id"`
	ResourceVersion string         `json:"resource_version"`
	URL             string         `json:"url"`
	Target          string         `json:"target"`
	Kind            string         `json:"kind"`
	Name            string         `json:"name"`
	TotalFileCount  int            `json:"total_file_count"`
	TotalSize       string         `json:"total_size"`
	Complete        bool           `json:"complete"`
	ExpiresAt       time.Time      `json:"expires_at"`
	Files           []ResourceFile `json:"files"`
}

type resourceSnapshot struct {
	preview          ResourcePreview
	account, userID  string
	session          uint64
	bytes            int
	indices          map[int64]int64
	mimeType, fileID string
}

type resourceBudgetKey struct{}
type resourceBudget struct{ requests, bytes int }

func resourceToken(d object) (string, error) {
	value, present := d["next_page_token"]
	if !present || value == nil {
		return "", nil
	}
	token, valid := value.(string)
	if !valid || len(token) > 4096 {
		return "", resourceError("分页标记无效")
	}
	return token, nil
}

func resourceError(reason string) error {
	return failure("resource_incomplete", "资源文件列表不完整或结构异常（"+reason+"），请重新解析")
}
func resourceTooLarge() error {
	return failure("resource_too_large", "资源超过本地完整预览上限，请使用迅雷客户端选择文件")
}

func (c *Client) clearPreviews() {
	c.previews = nil
	c.previewBytes = 0
	c.resourceSession++
}

func (c *Client) ResolveResource(ctx context.Context, req ResolveRequest) (ResourcePreview, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		return ResourcePreview{}, err
	}
	defer c.unlock()
	if err := c.checkTarget(req.Target); err != nil {
		return ResourcePreview{}, err
	}
	link, err := NormalizeLink(req.URL)
	if err != nil {
		return ResourcePreview{}, err
	}
	if err = c.ensureToken(ctx); err != nil {
		return ResourcePreview{}, err
	}
	s := c.snapshot()
	for id, p := range c.previews {
		if !time.Now().Before(p.preview.ExpiresAt) {
			delete(c.previews, id)
			c.previewBytes -= p.bytes
		} else if p.account == s.Username && p.userID == s.UserID && p.session == c.resourceSession && p.preview.Target == req.Target && p.preview.URL == link {
			return clonePreview(p.preview), nil
		}
	}
	if len(c.previews) >= maxPreviews {
		return ResourcePreview{}, failure("rate_limited", "有效资源预览已达上限，请等待预览过期后重试")
	}
	p, err := c.parseResource(ctx, req.Target, link)
	if err != nil {
		return ResourcePreview{}, err
	}
	if err = ctx.Err(); err != nil {
		return ResourcePreview{}, err
	}
	b, err := json.Marshal(p.preview)
	if err != nil || len(b) > maxPreviewBytes {
		return ResourcePreview{}, resourceTooLarge()
	}
	if c.previewBytes+len(b) > maxPreviewCacheBytes {
		return ResourcePreview{}, failure("rate_limited", "资源预览缓存已满，请等待预览过期后重试")
	}
	p.bytes = len(b)
	if c.previews == nil {
		c.previews = make(map[string]*resourceSnapshot)
	}
	c.previews[p.preview.PreviewID] = p
	c.previewBytes += p.bytes
	return clonePreview(p.preview), nil
}

func clonePreview(p ResourcePreview) ResourcePreview {
	p.Files = append([]ResourceFile(nil), p.Files...)
	for i := range p.Files {
		if p.Files[i].Index != nil {
			n := *p.Files[i].Index
			p.Files[i].Index = &n
		}
	}
	return p
}

type resourcePage struct {
	parent, token string
	depth         int
	seen          map[string]bool
}
type resourceBuilder struct {
	nodes   map[string]ResourceFile
	counts  map[string]int64
	pages   []resourcePage
	indices map[int64]int64
	roots   []string
}

func (b *resourceBuilder) add(resources []any, parent string, depth int, single bool) error {
	if len(resources) > 0 && depth > maxResourceDepth {
		return resourceTooLarge()
	}
	for _, value := range resources {
		r, ok := value.(map[string]any)
		if !ok {
			return resourceError("资源节点格式无效")
		}
		if e, present := obj(r["meta"])["error"]; present && e != nil && e != "" && str(e) != "0" {
			return failure("resource_unavailable", "迅雷未能获取此资源的完整文件信息，请重新解析")
		}
		id := str(r["id"])
		if id == "" && single && parent == "" && len(resources) == 1 {
			id = "single"
		}
		if id == "" {
			return resourceError("资源节点缺少标识")
		}
		if p := str(r["parent_id"]); p != "" && p != parent {
			return resourceError("目录与父级关系不一致")
		}
		isDir := false
		if v, present := r["is_dir"]; present {
			var valid bool
			isDir, valid = v.(bool)
			if !valid {
				return resourceError("文件或目录类型无效")
			}
		} else {
			_, isDir = r["dir"]
		}
		name := first(str(r["name"]), str(r["file_name"]))
		if name == "" {
			return resourceError("资源名称缺失")
		}
		size, hasSize, err := integerField(r, "file_size")
		if err != nil || size < 0 || (!hasSize && !isDir) {
			return resourceError("文件大小缺失或无效")
		}
		n := ResourceFile{ID: id, ParentID: parent, Name: name, IsDir: isDir, Size: strconv.FormatInt(size, 10)}
		if !isDir {
			index, hasIndex, err := integerField(r, "file_index")
			if err != nil || (hasIndex && (index < 0 || index > maxFileIndex)) || (!hasIndex && !single) {
				return resourceError("文件索引缺失或无效")
			}
			if hasIndex {
				n.Index = &index
			}
			token, err := resourceToken(obj(r["dir"]))
			if err != nil || len(items(obj(r["dir"])["resources"])) > 0 || token != "" {
				return resourceError("文件节点包含子目录数据")
			}
		}
		count, hasCount, err := integerField(r, "file_count")
		if err != nil || count < 0 {
			return resourceError("资源文件数量无效")
		}
		if old, exists := b.nodes[id]; exists {
			a, _ := json.Marshal(old)
			c, _ := json.Marshal(n)
			oldCount, oldHasCount := b.counts[id]
			if string(a) != string(c) || oldHasCount != hasCount || (hasCount && oldCount != count) {
				return resourceError("相同节点包含冲突信息")
			}
		} else {
			if len(b.nodes) >= maxResourceNodes {
				return resourceTooLarge()
			}
			b.nodes[id] = n
			if parent == "" {
				b.roots = append(b.roots, id)
			}
			if hasCount {
				b.counts[id] = count
			}
			if !isDir {
				if n.Index != nil {
					if _, exists := b.indices[*n.Index]; exists {
						return resourceError("文件索引重复")
					}
					b.indices[*n.Index] = size
				}
				// 无索引直链也计入叶子上限，避免异常响应绕过限制。
				if len(b.indices) > maxResourceFiles {
					return resourceTooLarge()
				}
			}
		}
		if isDir {
			d, valid := r["dir"].(map[string]any)
			if !valid {
				return resourceError("目录缺少文件列表")
			}
			if children, present := d["resources"]; present && children != nil {
				if _, valid := children.([]any); !valid {
					return resourceError("目录文件列表格式无效")
				}
			}
			if err := b.add(items(d["resources"]), id, depth+1, false); err != nil {
				return err
			}
			token, err := resourceToken(d)
			if err != nil {
				return err
			}
			if token != "" {
				// 同一目录重复出现时只登记一次分页链。
				found := false
				for _, p := range b.pages {
					if p.parent == id {
						if p.token != token {
							return resourceError("目录分页标记冲突")
						}
						found = true
						break
					}
				}
				if !found {
					b.pages = append(b.pages, resourcePage{parent: id, token: token, depth: depth + 1, seen: map[string]bool{}})
				}
			}
		}
	}
	return nil
}

func (c *Client) parseResource(ctx context.Context, target, link string) (*resourceSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, resourceBudgetKey{}, &resourceBudget{})
	d, err := c.request(ctx, "POST", driveURL+"/resource/list", nil, object{"page_size": 2000, "urls": link})
	if err != nil {
		return nil, err
	}
	list := obj(d["list"])
	resources := items(list["resources"])
	if len(resources) == 0 {
		return nil, failure("resource_unavailable", "迅雷未能解析此链接，请检查链接后重新解析")
	}
	root := obj(resources[0])
	b := resourceBuilder{nodes: map[string]ResourceFile{}, counts: map[string]int64{}, indices: map[int64]int64{}}
	if err = b.add(resources, "", 1, !isMagnetLink(link)); err != nil {
		return nil, err
	}
	token, err := resourceToken(list)
	if err != nil {
		return nil, err
	}
	if token != "" {
		b.pages = append(b.pages, resourcePage{token: token, depth: 1, seen: map[string]bool{}})
	}
	listID, _ := d["list_id"].(string)
	for i := 0; i < len(b.pages); i++ {
		p := b.pages[i]
		for p.token != "" {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			if listID == "" || len(listID) > 256 || strings.ContainsAny(listID, "/\\?#") || listID == "." || listID == ".." || len(p.token) > 4096 || p.seen[p.token] {
				return nil, resourceError("资源分页缺少有效标记或发生循环")
			}
			p.seen[p.token] = true
			d, err = c.request(ctx, "GET", driveURL+"/resource/list/"+url.PathEscape(listID), url.Values{"page_token": {p.token}}, nil)
			if err != nil {
				return nil, err
			}
			list = obj(d["list"])
			if _, valid := list["resources"].([]any); !valid {
				return nil, resourceError("分页响应缺少文件列表")
			}
			if err = b.add(items(list["resources"]), p.parent, p.depth, false); err != nil {
				return nil, err
			}
			p.token, err = resourceToken(list)
			if err != nil {
				return nil, err
			}
		}
	}
	if len(b.roots) != 1 {
		return nil, resourceError("链接未对应唯一根资源")
	}
	p := &resourceSnapshot{indices: b.indices, mimeType: str(root["mime_type"]), fileID: str(root["file_id"])}
	p.preview = ResourcePreview{PreviewID: randomID(), URL: link, Target: target, Kind: "files", Name: b.nodes[b.roots[0]].Name, Complete: true, ExpiresAt: time.Now().Add(previewLifetime), Files: []ResourceFile{}}
	type leafVersion struct {
		Index *int64
		Path  []string
		Size  string
	}
	leaves := []leafVersion{}
	var total int64
	for _, n := range b.nodes {
		p.preview.Files = append(p.preview.Files, n)
		if n.IsDir {
			continue
		}
		p.preview.TotalFileCount++
		if p.preview.TotalFileCount > maxResourceFiles {
			return nil, resourceTooLarge()
		}
		size, _ := strconv.ParseInt(n.Size, 10, 64)
		if size > math.MaxInt64-total {
			return nil, resourceError("资源总大小溢出")
		}
		total += size
		segments := []string{n.Name}
		parent := n.ParentID
		seen := map[string]bool{n.ID: true}
		for parent != "" {
			pn, exists := b.nodes[parent]
			if !exists || !pn.IsDir || seen[parent] || len(segments) >= maxResourceDepth {
				return nil, resourceError("文件路径缺少父级或发生循环")
			}
			seen[parent] = true
			segments = append(segments, pn.Name)
			parent = pn.ParentID
		}
		for i, j := 0, len(segments)-1; i < j; i, j = i+1, j-1 {
			segments[i], segments[j] = segments[j], segments[i]
		}
		leaves = append(leaves, leafVersion{n.Index, segments, n.Size})
	}
	// 官方创建参数仅使用根资源的文件总数。叶子及子目录摘要的计数语义未确认，
	// 不能把其零值或子项数当作递归叶子总数，以免拒绝已经完整加载的文件树。
	if count, present := b.counts[b.roots[0]]; present && count != int64(p.preview.TotalFileCount) {
		return nil, resourceError("根资源文件总数与完整列表不一致")
	}
	if p.preview.TotalFileCount == 0 {
		return nil, resourceError("资源没有可下载文件")
	}
	if len(leaves) == 1 && !isMagnetLink(link) && !b.nodes[b.roots[0]].IsDir {
		p.preview.Kind = "single"
		// 普通直链只有一个对象时，下载身份不依赖可选的子文件索引。
		leaves[0].Index = nil
		for i := range p.preview.Files {
			p.preview.Files[i].Index = nil
		}
	} else if len(b.indices) != len(leaves) {
		return nil, resourceError("文件索引列表不完整")
	}
	p.preview.TotalSize = strconv.FormatInt(total, 10)
	sort.Slice(leaves, func(i, j int) bool {
		if leaves[i].Index == nil {
			return false
		}
		return *leaves[i].Index < *leaves[j].Index
	})
	version, _ := json.Marshal(struct {
		Kind   string
		Count  int
		Leaves []leafVersion
	}{p.preview.Kind, p.preview.TotalFileCount, leaves})
	digest := sha256.Sum256(version)
	p.preview.ResourceVersion = hex.EncodeToString(digest[:])
	sort.Slice(p.preview.Files, func(i, j int) bool { return p.preview.Files[i].ID < p.preview.Files[j].ID })
	s := c.snapshot()
	p.account = s.Username
	p.userID = s.UserID
	p.session = c.resourceSession
	p.preview.ExpiresAt = time.Now().Add(previewLifetime)
	return p, nil
}
