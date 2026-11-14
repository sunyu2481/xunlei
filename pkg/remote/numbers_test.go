package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"testing"
)

func Test任务整数数值表示(t *testing.T) {
	for _, tt := range []struct {
		name                         string
		fields                       object
		size, done, speed, createdAt int64
		progress                     float64
	}{
		{"小数形式整数", object{"file_size": json.Number("1000.0"), "progress": "37.5", "created_time": json.Number("1770000000.0"), "params": object{"download_size": "350.0", "speed": json.Number("128.0")}}, 1000, 350, 128, 1770000000, 37.5},
		{"科学计数整数", object{"file_size": "1e3", "status_size": json.Number("3.5e2"), "download_speed": "1.28e2", "created_time": "1.77e12"}, 1000, 350, 128, 1770000000, 35},
		{"科学计数缩小尾零", object{"file_size": "100000e-2", "status_size": "0.0350e4", "download_speed": "000128.000e0"}, 1000, 350, 128, 0, 35},
		{"空白数值字符串", object{"file_size": " 1000 ", "status_size": " 350 ", "download_speed": " 128 ", "created_time": " 1770000000 "}, 1000, 350, 128, 1770000000, 35},
		{"小数形式校验字节", object{"file_size": "1000", "params": object{"download_size": "1000", "checked_size": "400.0"}}, 1000, 1000, 0, 0, 40},
		{"二进制精度之外", object{"file_size": json.Number("9007199254740993.0"), "status_size": "9007199254740993e0", "download_speed": "9007199254740993.0"}, 9007199254740993, 9007199254740993, 9007199254740993, 0, 100},
		{"整数上限", object{"file_size": "9223372036854775807.0", "status_size": "9223372036854775807", "download_speed": "9223372036854775807e0"}, math.MaxInt64, math.MaxInt64, math.MaxInt64, 0, 100},
		{"零值", object{"file_size": "0.0", "status_size": "0e9", "download_speed": 0, "created_time": 0}, 0, 0, 0, 0, 0},
		{"省略可选数值", object{}, 0, 0, 0, 0, 0},
		{"空字符串沿用备用字段", object{"file_size": "1000", "created_time": "", "params": object{"download_size": "", "speed": "", "checked_size": ""}, "status_size": "350", "download_speed": "128"}, 1000, 350, 128, 0, 35},
		{"负数字节保持归零", object{"file_size": "-1.0", "status_size": "-2e0", "download_speed": "-3"}, 0, 0, 0, 0, 0},
		{"整数下限", object{"file_size": "-9223372036854775808.0", "status_size": "-9223372036854775808e0"}, 0, 0, 0, 0, 0},
		{"字符串时间", object{"created_time": "2026-02-02T02:40:00Z"}, 0, 0, 0, 1770000000, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				task := object{"id": "任务一", "type": "user#download", "space": "设备一"}
				for k, v := range tt.fields {
					task[k] = v
				}
				respond(w, object{"tasks": []any{task}})
			})
			page, err := c.Tasks(context.Background(), "设备一", "")
			if err != nil || len(page.Tasks) != 1 {
				t.Fatalf("读取任务失败：%v，%v", page, err)
			}
			got := page.Tasks[0]
			if got.Size != tt.size || got.Done != tt.done || got.Speed != tt.speed || got.createdAt != tt.createdAt || got.Progress != tt.progress {
				t.Fatalf("数值解析错误：%+v", got)
			}
		})
	}
}

func Test任务无效整数不能当作零(t *testing.T) {
	for _, field := range []string{"file_size", "status_size", "download_speed", "created_time", "download_size", "speed", "checked_size"} {
		for _, value := range []any{json.Number("0.1"), "9223372036854775808", "-9223372036854775809", "1e999999999", "1e-999999999", "9007199254740992.1", "NaN", "Inf", nil, true, object{}, []any{}} {
			t.Run(fmt.Sprintf("%s=%v/%T", field, value, value), func(t *testing.T) {
				c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
					task := object{"id": "任务一", "type": "user#download", "space": "设备一", "file_size": "1000", "status_size": "1000", "download_speed": "50"}
					switch field {
					case "download_size", "speed", "checked_size":
						task["params"] = object{field: value}
					default:
						task[field] = value
					}
					respond(w, object{"tasks": []any{task}})
				})
				page, err := c.Tasks(context.Background(), "设备一", "")
				wantKind(t, err, "upstream")
				if len(page.Tasks) != 0 {
					t.Fatal("无效整数不应返回伪造为零的任务")
				}
			})
		}
	}
}

func Test令牌有效期保留数值解析错误(t *testing.T) {
	for _, value := range []any{"0.1", "9223372036854775808", "9223372036854775807", true, nil} {
		t.Run(str(value), func(t *testing.T) {
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("不应发送请求") })
			before := c.snapshot()
			err := c.applyToken(object{"access_token": "新令牌", "expires_in": value})
			wantKind(t, err, "upstream")
			if !reflect.DeepEqual(c.snapshot(), before) {
				t.Fatal("无效有效期不应更改令牌状态")
			}
		})
	}
}
