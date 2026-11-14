package flags

import (
	"os"
	"reflect"
	"testing"

	"github.com/spf13/pflag"
)

func Test环境值与重复参数优先级(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     string
		args    []string
		want    []int
		wantErr bool
	}{
		{"只用环境值", "1,2", nil, []int{1, 2}, false},
		{"命令行替换后追加", "1,2", []string{"--items=3", "--items=4,5"}, []int{3, 4, 5}, false},
		{"非法环境值", "错误", nil, nil, true},
		{"合法参数覆盖非法环境值", "错误", []string{"--items=3"}, []int{3}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XL_FLAGS_TEST_ITEMS", tc.env)
			old := os.Args
			t.Cleanup(func() { os.Args = old })
			os.Args = append([]string{"测试"}, tc.args...)
			fs := pflag.NewFlagSet("测试", pflag.ContinueOnError)
			var items []int
			VarFlag(fs, &items, "items", "", "测试参数", "XL_FLAGS_TEST_ITEMS")
			err := ParseFlag(fs)
			if (err != nil) != tc.wantErr {
				t.Fatalf("解析错误：%v", err)
			}
			if err == nil && (!reflect.DeepEqual(items, tc.want) || fs.Changed("items") != (len(tc.args) > 0)) {
				t.Fatalf("值=%v，命令行设置=%t", items, fs.Changed("items"))
			}
		})
	}
}
