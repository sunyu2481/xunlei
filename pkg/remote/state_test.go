package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func Test空闲锁拒绝已取消操作(t *testing.T) {
	for _, operation := range []string{"登录", "退出"} {
		for _, expired := range []bool{false, true} {
			name := operation + "/取消"
			if expired {
				name = operation + "/超时"
			}
			t.Run(name, func(t *testing.T) {
				calls := 0
				c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					respond(w, object{"error": "invalid_grant"})
				})
				c.update(func(s *state) {
					s.Username = "原账号"
					s.ReviewCookies = map[string]string{"token": "原验证令牌"}
					s.Submissions = map[string]submission{"原记录": {TaskID: "原任务"}}
				})
				c.relays["设备一"] = relay{Base: "https://relay.xunlei.com"}
				before, relays := copyState(c.snapshot()), c.relays
				if err := c.save(); err != nil {
					t.Fatal(err)
				}
				disk := stateBytes(t, c.path)
				ctx, cancel := context.WithCancel(context.Background())
				want := context.Canceled
				if expired {
					cancel()
					ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
					want = context.DeadlineExceeded
				}
				cancel()
				for i := 0; i < 128; i++ {
					var err error
					if operation == "登录" {
						err = c.Login(ctx, "新账号", "新密码", true)
					} else {
						err = c.Logout(ctx)
					}
					if !errors.Is(err, want) {
						t.Errorf("第 %d 次操作未返回上下文错误：%v", i+1, err)
					}
					if !reflect.DeepEqual(c.snapshot(), before) || !reflect.DeepEqual(c.relays, relays) || !bytes.Equal(stateBytes(t, c.path), disk) || calls != 0 {
						t.Fatal("已取消操作改变了会话、缓存、状态文件或发送了请求")
					}
				}
				if err := c.Logout(context.Background()); err != nil {
					t.Fatalf("取消操作泄漏了操作锁：%v", err)
				}
			})
		}
	}
}

// 在取得锁后的检查点取消，稳定覆盖等待结束与取消同时发生的窗口。
type cancelAfterLockContext struct {
	context.Context
	gate   chan struct{}
	cancel context.CancelFunc
}

func (c cancelAfterLockContext) Err() error {
	if len(c.gate) != 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func Test取得操作锁后复查取消(t *testing.T) {
	c := mockClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("不应发送请求") })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := c.lock(cancelAfterLockContext{Context: ctx, gate: c.gate, cancel: cancel})
	if err == nil {
		c.unlock()
		t.Fatal("取得锁后没有复查取消")
	}
	if !errors.Is(err, context.Canceled) || len(c.gate) != 0 {
		t.Fatalf("取消后未返回原错误或未释放锁：%v", err)
	}
	if err := c.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.unlock()
}

type stateFaults struct {
	fail    string
	dir     string
	steps   []string
	synced  []string
	handles []*stateFaultFile
}

func (f *stateFaults) step(name string) error {
	f.steps = append(f.steps, name)
	if f.fail == name {
		return errors.New("模拟保存步骤故障")
	}
	return nil
}

func (f *stateFaults) install(c *Client) {
	f.dir = filepath.Dir(c.path)
	local := localStateFiles()
	c.files = stateFiles{
		mkdirAll: func(path string, mode os.FileMode) error {
			if err := f.step("创建目录"); err != nil {
				return err
			}
			return local.mkdirAll(path, mode)
		},
		chmod: func(path string, mode os.FileMode) error {
			if err := f.step("目录权限"); err != nil {
				return err
			}
			return local.chmod(path, mode)
		},
		createTemp: func(dir, pattern string) (stateFile, error) {
			if err := f.step("创建临时文件"); err != nil {
				return nil, err
			}
			file, err := local.createTemp(dir, pattern)
			if err != nil {
				return nil, err
			}
			return f.wrap(file, "文件"), nil
		},
		rename: func(old, new string) error {
			if err := f.step("替换文件"); err != nil {
				return err
			}
			return local.rename(old, new)
		},
		open: func(path string) (stateFile, error) {
			kind := "父目录"
			if path == f.dir {
				kind = "状态目录"
			}
			if err := f.step(kind + "打开"); err != nil {
				return nil, err
			}
			file, err := local.open(path)
			if err != nil {
				return nil, err
			}
			return f.wrap(file, kind), nil
		},
	}
}

func (f *stateFaults) wrap(file stateFile, kind string) stateFile {
	handle := &stateFaultFile{stateFile: file, faults: f, kind: kind}
	f.handles = append(f.handles, handle)
	return handle
}

type stateFaultFile struct {
	stateFile
	faults *stateFaults
	kind   string
	closed bool
}

func (f *stateFaultFile) Write(b []byte) (int, error) {
	if err := f.faults.step("文件写入"); err != nil {
		return 0, err
	}
	if f.faults.fail == "部分写入" {
		n, err := f.stateFile.Write(b[:len(b)/2])
		if err == nil {
			err = io.ErrShortWrite
		}
		return n, err
	}
	return f.stateFile.Write(b)
}

func (f *stateFaultFile) Sync() error {
	if err := f.faults.step(f.kind + "同步"); err != nil {
		return err
	}
	if err := f.stateFile.Sync(); err != nil {
		return err
	}
	if f.kind != "文件" {
		f.faults.synced = append(f.faults.synced, f.Name())
	}
	return nil
}

func (f *stateFaultFile) Close() error {
	if f.closed {
		return f.stateFile.Close()
	}
	f.closed = true
	err := f.stateFile.Close()
	if fault := f.faults.step(f.kind + "关闭"); fault != nil {
		return fault
	}
	return err
}

func (f *stateFaults) checkCleanup(t *testing.T) {
	t.Helper()
	for _, handle := range f.handles {
		if !handle.closed {
			t.Fatal("保存返回后仍有未关闭的文件或目录")
		}
	}
	paths, err := filepath.Glob(filepath.Join(f.dir, ".state-*"))
	if err != nil || len(paths) != 0 {
		t.Fatalf("临时状态文件未清理：%v", err)
	}
}

func Test状态保存步骤故障(t *testing.T) {
	for _, step := range []string{
		"创建目录", "目录权限", "创建临时文件", "文件写入", "部分写入", "文件同步", "文件关闭", "替换文件",
		"状态目录打开", "状态目录同步", "状态目录关闭", "父目录打开", "父目录同步", "父目录关闭",
	} {
		t.Run(step, func(t *testing.T) {
			c := mockClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("不应发送请求") })
			c.update(func(s *state) {
				s.Username, s.Remember = "原账号", true
				s.Submissions = map[string]submission{"原记录": {TaskID: "原任务"}}
			})
			if err := c.save(); err != nil {
				t.Fatal(err)
			}
			before, disk := copyState(c.snapshot()), stateBytes(t, c.path)
			faults := &stateFaults{fail: step}
			faults.install(c)
			err := c.updateAndSave(func(s *state) error {
				s.Username = "新账号"
				s.Submissions["新记录"] = submission{TaskID: "新任务"}
				return nil
			})
			if err == nil {
				t.Fatalf("没有报告%s故障", step)
			}
			replaced := strings.HasPrefix(step, "状态目录") || strings.HasPrefix(step, "父目录")
			if replaced {
				saved, readErr := readState(c.path)
				if readErr != nil || saved.Username != "新账号" || !reflect.DeepEqual(saved, c.snapshot()) {
					t.Fatal("替换后的同步失败使内存与已替换的文件不一致")
				}
				if !strings.Contains(err.Error(), "已替换") {
					t.Fatal("替换后故障没有说明状态已经替换")
				}
			} else if !reflect.DeepEqual(c.snapshot(), before) || !bytes.Equal(stateBytes(t, c.path), disk) {
				t.Fatal("替换前故障改变了内存或原状态文件")
			}
			faults.checkCleanup(t)
			faults.fail = ""
			if err := c.save(); err != nil {
				t.Fatalf("故障消除后不能保存：%v", err)
			}
			faults.checkCleanup(t)
		})
	}
}

func Test状态保存同步新建目录链(t *testing.T) {
	for _, fail := range []string{"", "父目录同步"} {
		name := "直接成功"
		if fail != "" {
			name = "父目录同步失败后重试"
		}
		t.Run(name, func(t *testing.T) {
			c := mockClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("不应发送请求") })
			c.path = filepath.Join(t.TempDir(), "新建", "多级", "remote", "state.json")
			faults := &stateFaults{fail: fail}
			faults.install(c)
			if fail != "" {
				if err := c.save(); err == nil {
					t.Fatal("新建目录链同步失败未报告错误")
				}
				faults.checkCleanup(t)
				faults.fail, faults.steps, faults.synced = "", nil, nil
			}
			if err := c.save(); err != nil {
				t.Fatal(err)
			}
			for dir := filepath.Dir(c.path); ; dir = filepath.Dir(dir) {
				if !slices.Contains(faults.synced, dir) {
					t.Fatal("成功保存未同步状态目录或其祖先目录")
				}
				if dir == filepath.Dir(dir) {
					break
				}
			}
			fileSync := slices.Index(faults.steps, "文件同步")
			fileClose := slices.Index(faults.steps, "文件关闭")
			rename := slices.Index(faults.steps, "替换文件")
			dirSync := slices.Index(faults.steps, "状态目录同步")
			if fileSync < 0 || fileClose <= fileSync || rename <= fileClose || dirSync <= rename {
				t.Fatal("保存必须依次完成文件同步、关闭、替换和状态目录同步")
			}
			faults.checkCleanup(t)
		})
	}
}

func Test登录目录同步失败采用已替换账号(t *testing.T) {
	for _, username := range []string{"原账号", "新账号"} {
		t.Run(username, func(t *testing.T) {
			calls := 0
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch r.URL.Path {
				case "/xluser.core.login/v3/login":
					respond(w, object{"sessionID": "新会话"})
				case "/v1/shield/captcha/init":
					respond(w, object{"captcha_token": "新人机令牌"})
				case "/v1/auth/signin/token":
					respond(w, object{"access_token": "新访问令牌", "refresh_token": "新刷新令牌"})
				default:
					t.Fatalf("意外请求：%s", r.URL.Path)
				}
			})
			c.update(func(s *state) { s.Username = "原账号" })
			c.relays["设备一"] = relay{Base: "https://relay.xunlei.com"}
			faults := &stateFaults{fail: "状态目录同步"}
			faults.install(c)
			if err := c.Login(context.Background(), username, "新密码", false); err == nil {
				t.Fatal("登录未报告目录同步失败")
			}
			s := c.snapshot()
			saved, err := readState(c.path)
			if err != nil || s.Username != username || s.AccessToken != "" || s.RefreshToken != "" || s.Password != "新密码" || calls != 0 {
				t.Fatal("同步失败后未采用已替换的登录准备状态，或仍发送了请求")
			}
			s.Password = ""
			if !reflect.DeepEqual(s, saved) {
				t.Fatal("磁盘与内存的账号不一致，或未记住的密码落盘")
			}
			if (username == "新账号" && len(c.relays) != 0) || (username == "原账号" && len(c.relays) != 1) {
				t.Fatal("中继缓存没有随已替换的账号更新")
			}
			faults.fail = ""
			if err := c.Login(context.Background(), username, "新密码", false); err != nil || calls != 3 {
				t.Fatalf("存储恢复后不能继续登录：%v", err)
			}
			faults.checkCleanup(t)
		})
	}
}

func Test设置目录同步失败采用已替换设置(t *testing.T) {
	c := mockClient(t, driveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/drive/v1/tasks" {
			return false
		}
		respond(w, object{"tasks": []any{object{"name": "所选设备", "params": object{"target": "设备一"}}}})
		return true
	}))
	c.update(func(s *state) { s.TargetName, s.Directory = "原设备", "/downloads/原目录" })
	faults := &stateFaults{fail: "状态目录同步"}
	faults.install(c)
	settings := Settings{Target: "设备一", Directory: "/downloads/电影"}
	if err := c.Configure(context.Background(), settings); err == nil {
		t.Fatal("设置未报告目录同步失败")
	}
	saved, err := readState(c.path)
	if err != nil || saved.TargetName != "所选设备" || saved.Directory != settings.Directory || !reflect.DeepEqual(saved, c.snapshot()) {
		t.Fatal("同步失败后恢复了旧设置，与已替换文件不一致")
	}
	faults.fail = ""
	if err := c.Configure(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	faults.checkCleanup(t)
}

func Test创建目录同步失败保留提交证据(t *testing.T) {
	for _, sent := range []bool{false, true} {
		name := "发送前"
		if sent {
			name = "取得任务编号后"
		}
		t.Run(name, func(t *testing.T) {
			posted := 0
			faults := &stateFaults{}
			c := mockClient(t, driveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/drive/v1/task" {
					posted++
					faults.fail = "状态目录同步"
				}
				return false
			}))
			faults.install(c)
			if !sent {
				faults.fail = "状态目录同步"
			}
			_, err := c.Create(context.Background(), sampleRequest())
			if err == nil {
				t.Fatal("创建未报告目录同步失败")
			}
			wantPosts, wantID := 0, ""
			if sent {
				wantPosts, wantID = 1, "task-id"
				wantKind(t, err, "submission_unknown")
			}
			saved, err := readState(c.path)
			if err != nil || !reflect.DeepEqual(saved, c.snapshot()) {
				t.Fatal("目录同步失败后内存与已替换的提交记录不一致")
			}
			submitted := saved.Submissions[sampleRequest().RequestID]
			if submitted.Fingerprint == "" || submitted.TaskID != wantID || posted != wantPosts {
				t.Fatal("同步失败丢失提交证据，或仍继续发送创建请求")
			}
			faults.fail = ""
			restored, err := New(c.path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(restored.Close)
			restored.http.Transport = c.http.Transport
			for _, client := range []*Client{c, restored} {
				result, err := client.Create(context.Background(), sampleRequest())
				if sent {
					if err != nil || result.ID != wantID || !result.Reused {
						t.Fatalf("未复用已保存的任务编号：%v", err)
					}
				} else {
					wantKind(t, err, "submission_unknown")
				}
				if posted != wantPosts {
					t.Fatal("重试或重建客户端后重复创建任务")
				}
			}
			faults.checkCleanup(t)
		})
	}
}

func Test并发保存保持完整状态文件(t *testing.T) {
	c := mockClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("不应发送请求") })
	var invalid atomic.Int32
	done, readerDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			b, err := os.ReadFile(c.path)
			if err != nil || !json.Valid(b) {
				invalid.Add(1)
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	var group sync.WaitGroup
	for i := 0; i < 32; i++ {
		group.Go(func() {
			if err := c.updateAndSave(func(s *state) error { s.ExpiresAt++; return nil }); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	close(done)
	<-readerDone
	saved, err := readState(c.path)
	if err != nil || saved.ExpiresAt != 32 || !reflect.DeepEqual(saved, c.snapshot()) || invalid.Load() != 0 {
		t.Fatal("并发保存丢失状态更新或外部读取出现不完整文件")
	}
	for path, mode := range map[string]os.FileMode{c.path: 0600, filepath.Dir(c.path): 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("状态文件或目录权限不正确")
		}
	}
	(&stateFaults{dir: filepath.Dir(c.path)}).checkCleanup(t)
}
