package remote

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func reconciliationTask(id string, at int64) object {
	req := sampleRequest()
	return object{"id": id, "type": "user#download-url", "space": req.Target, "created_time": at,
		"params": object{"url": req.URL, "parent_folder_path": req.Directory + "/"}}
}

// 从已落盘的未知记录开始核对，并验证重建客户端后不会重复创建任务。
func checkReconciliation(t *testing.T, at int64, pages map[string]object, wantID string, wantPages int) {
	t.Helper()
	req := sampleRequest()
	calls := 0
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/drive/v1/tasks" || r.URL.Query().Get("space") != req.Target {
			t.Fatalf("核对请求越界或重复创建任务：%s %s", r.Method, r.URL.Path)
		}
		calls++
		page, ok := pages[r.URL.Query().Get("page_token")]
		if !ok {
			t.Fatalf("请求了未预期的分页令牌：%q", r.URL.Query().Get("page_token"))
		}
		respond(w, page)
	})
	sub := submission{Fingerprint: md5hex(req.Target + "\n" + req.Directory + "\n" + req.URL),
		Target: req.Target, URL: req.URL, Directory: req.Directory, At: at}
	c.update(func(s *state) { s.Submissions = map[string]submission{req.RequestID: sub} })
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	before, disk := copyState(c.snapshot()), stateBytes(t, c.path)
	for _, restart := range []bool{false, true} {
		if restart {
			restored, err := New(c.path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(restored.Close)
			restored.http.Transport = c.http.Transport
			c = restored
		}
		calls = 0
		result, err := c.Create(context.Background(), req)
		if wantID == "" {
			wantKind(t, err, "submission_unknown")
			if result != (CreateResult{}) || !reflect.DeepEqual(c.snapshot(), before) || !bytes.Equal(stateBytes(t, c.path), disk) {
				t.Fatal("无法确认唯一任务时改变了提交证据或保存了成功结果")
			}
		} else {
			if err != nil || result.ID != wantID || !result.Reused {
				t.Fatalf("未复用完整核对得到的唯一任务：%+v，%v", result, err)
			}
			want := copyState(before)
			confirmed := sub
			confirmed.TaskID = wantID
			want.Submissions[req.RequestID] = confirmed
			saved, err := readState(c.path)
			if err != nil || !reflect.DeepEqual(saved, want) || !reflect.DeepEqual(c.snapshot(), want) {
				t.Fatal("核对后未完整保留原提交并保存唯一任务编号")
			}
		}
		wantCalls := wantPages
		if restart && wantID != "" {
			wantCalls = 0
		}
		if calls != wantCalls {
			t.Fatalf("重建=%t，预期读取%d页，实际%d页", restart, wantCalls, calls)
		}
	}
}

func Test核对使用原提交固定时间窗口(t *testing.T) {
	at := time.Now().Add(-72 * time.Hour).Unix()
	for _, tc := range []struct {
		name    string
		created int64
		wantID  string
	}{
		{"早于下限一秒", at - 6, ""},
		{"允许提前五秒", at - 5, "候选"},
		{"原提交时刻", at, "候选"},
		{"允许延迟六十秒", at + 60, "候选"},
		{"晚于上限一秒", at + 61, ""},
		{"三天后另行创建", time.Now().Unix(), ""},
		{"原提交前一小时", at - 3600, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkReconciliation(t, at, map[string]object{"": {"tasks": []any{reconciliationTask("候选", tc.created)}}}, tc.wantID, 1)
		})
	}
}

func Test核对要求完整分页与唯一任务(t *testing.T) {
	at := time.Now().Unix()
	for _, tc := range []struct {
		name      string
		pages     map[string]object
		wantID    string
		wantPages int
	}{
		{"单页唯一匹配", map[string]object{"": {"tasks": []any{reconciliationTask("候选甲", at)}}}, "候选甲", 1},
		{"没有匹配", map[string]object{"": {"tasks": []any{}}}, "", 1},
		{"空尾页正常结束", map[string]object{
			"":  {"tasks": []any{reconciliationTask("候选甲", at)}, "next_page_token": "乙"},
			"乙": {"tasks": []any{}},
		}, "候选甲", 2},
		{"同一编号跨页重复", map[string]object{
			"":  {"tasks": []any{reconciliationTask("候选甲", at)}, "next_page_token": "乙"},
			"乙": {"tasks": []any{reconciliationTask("候选甲", at)}},
		}, "候选甲", 2},
		{"不同编号同页歧义", map[string]object{
			"": {"tasks": []any{reconciliationTask("候选甲", at), reconciliationTask("候选乙", at)}},
		}, "", 1},
		{"不同编号跨页歧义", map[string]object{
			"":  {"tasks": []any{reconciliationTask("候选甲", at)}, "next_page_token": "乙"},
			"乙": {"tasks": []any{reconciliationTask("候选乙", at)}},
		}, "", 2},
		{"后页读取失败", map[string]object{
			"":  {"tasks": []any{reconciliationTask("候选甲", at)}, "next_page_token": "乙"},
			"乙": {"error_code": 100, "error": "rejected"},
		}, "", 2},
		{"分页令牌连续重复", map[string]object{
			"":  {"tasks": []any{reconciliationTask("候选甲", at)}, "next_page_token": "乙"},
			"乙": {"tasks": []any{}, "next_page_token": "乙"},
		}, "", 2},
		{"分页令牌交替循环", map[string]object{
			"":  {"tasks": []any{reconciliationTask("候选甲", at)}, "next_page_token": "乙"},
			"乙": {"tasks": []any{}, "next_page_token": "丙"},
			"丙": {"tasks": []any{}, "next_page_token": "乙"},
		}, "", 3},
		{"分页令牌跨三页循环", map[string]object{
			"":  {"tasks": []any{reconciliationTask("候选甲", at)}, "next_page_token": "乙"},
			"乙": {"tasks": []any{}, "next_page_token": "丙"},
			"丙": {"tasks": []any{}, "next_page_token": "丁"},
			"丁": {"tasks": []any{}, "next_page_token": "乙"},
		}, "", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkReconciliation(t, at, tc.pages, tc.wantID, tc.wantPages)
		})
	}
	for _, complete := range []bool{false, true} {
		name := "第五页仍有下一页"
		wantID := ""
		if complete {
			name, wantID = "第五页正常结束", "候选甲"
		}
		t.Run(name, func(t *testing.T) {
			pages := map[string]object{}
			token := ""
			for i := 1; i <= 5; i++ {
				next := fmt.Sprintf("第%d页", i+1)
				page := object{"tasks": []any{}, "next_page_token": next}
				if i == 1 || i == 5 {
					page["tasks"] = []any{reconciliationTask("候选甲", at)}
				}
				if complete && i == 5 {
					page["next_page_token"] = ""
				}
				pages[token], token = page, next
			}
			pages[token] = object{"tasks": []any{reconciliationTask("候选乙", at)}}
			checkReconciliation(t, at, pages, wantID, 5)
		})
	}
}

func Test提交记录同编号惰性留存(t *testing.T) {
	at := time.Now().Add(-8 * 24 * time.Hour).Unix()
	t.Run("已知编号重建后直接复用", func(t *testing.T) {
		req := sampleRequest()
		c := mockClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("复用已知编号不应请求上游") })
		c.update(func(s *state) {
			s.Submissions = map[string]submission{req.RequestID: {
				Fingerprint: md5hex(req.Target + "\n" + req.Directory + "\n" + req.URL),
				Target:      req.Target, URL: req.URL, Directory: req.Directory, At: at, TaskID: "八天前的任务",
			}}
		})
		if err := c.save(); err != nil {
			t.Fatal(err)
		}
		before, disk := copyState(c.snapshot()), stateBytes(t, c.path)
		restored, err := New(c.path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(restored.Close)
		restored.http.Transport = c.http.Transport
		for _, client := range []*Client{c, restored} {
			result, err := client.Create(context.Background(), req)
			if err != nil || result.ID != "八天前的任务" || !result.Reused || !reflect.DeepEqual(client.snapshot(), before) || !bytes.Equal(stateBytes(t, c.path), disk) {
				t.Fatalf("重建或同编号复用时清理了旧记录：%+v，%v", result, err)
			}
		}
	})
	t.Run("未知编号重建后保留证据", func(t *testing.T) {
		checkReconciliation(t, at, map[string]object{"": {"tasks": []any{}}}, "", 1)
	})
}

func Test提交记录新编号成功保存时清理(t *testing.T) {
	posted := 0
	c := mockClient(t, driveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/drive/v1/task" {
			posted++
		}
		return false
	}))
	recent := submission{At: time.Now().Add(-6 * 24 * time.Hour).Unix()}
	c.update(func(s *state) {
		s.Submissions = map[string]submission{
			"七天内的记录": recent,
			"八天前的记录": {At: time.Now().Add(-8 * 24 * time.Hour).Unix(), TaskID: "旧任务"},
		}
	})
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	before, disk := copyState(c.snapshot()), stateBytes(t, c.path)
	restore := blockStateSave(t, c)
	defer restore()
	if _, err := c.Create(context.Background(), sampleRequest()); err == nil {
		t.Fatal("发送前保存故障未返回错误")
	}
	restore()
	if posted != 0 || !reflect.DeepEqual(c.snapshot(), before) || !bytes.Equal(stateBytes(t, c.path), disk) {
		t.Fatal("新编号保存失败仍清理了旧记录或发送了创建请求")
	}
	result, err := c.Create(context.Background(), sampleRequest())
	if err != nil || result.ID != "task-id" || result.Reused || posted != 1 {
		t.Fatalf("新编号恢复后未成功提交：%+v，%v", result, err)
	}
	saved, err := readState(c.path)
	if err != nil || !reflect.DeepEqual(saved, c.snapshot()) || len(saved.Submissions) != 2 || saved.Submissions["七天内的记录"] != recent || saved.Submissions[sampleRequest().RequestID].TaskID != result.ID {
		t.Fatal("成功保存新编号后未清理过期记录或丢失了有效记录")
	}
}
