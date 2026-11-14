package flags

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cnk3x/xunlei/pkg/utils"
	"github.com/spf13/pflag"
)

func init() {
	pflag.ErrHelp = fmt.Errorf("\nstart with %s [...OPTIONS]", filepath.Base(os.Args[0]))
	pflag.Usage = func() {
		fmt.Fprint(os.Stderr, filepath.Base(os.Args[0]))
		if version != "" {
			fmt.Fprintf(os.Stderr, " - version %s", version)
		}
		if !buildTime.IsZero() {
			fmt.Fprintf(os.Stderr, " - build %s", buildTime.In(time.Local).Format(time.RFC3339))
		}
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "wrap system env as synology for xunlei")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "OPTIONS:")
		pflag.PrintDefaults()
	}
}

var (
	CommandLine = pflag.CommandLine

	version   string
	buildTime time.Time
)

func SetBuildTime(t time.Time) { buildTime = t }
func SetVersion(v string)      { version = v }
func GetBuildTime() time.Time  { return buildTime }

// GetVersion 返回 SetVersion 设置的帮助信息版本，未设置时为空。
func GetVersion() string { return version }

type FlagSet = pflag.FlagSet

func Var[T any](v T, name, short, usage string, env ...string) {
	VarFlag(CommandLine, v, name, short, usage, env...)
}

// SecretVar 绑定敏感字符串，保留解析和覆盖语义，但不在帮助中展示默认值。
func SecretVar(v *string, name, short, usage string, env ...string) {
	VarFlag(CommandLine, v, name, short, usage, env...)
	CommandLine.Lookup(name).DefValue = ""
}

func Parse() (err error) { return ParseFlag(CommandLine) }

func VarFlag[T any](fSet *pflag.FlagSet, v T, name, short, usage string, env ...string) {
	var envName, s string
	if len(env) > 0 {
		for _, key := range env {
			if value := os.Getenv(key); value != "" {
				envName, s = key, value
				break
			}
		}
		usage = fmt.Sprintf("%s%s[%s]", usage, utils.Iif(usage != "", " ", ""), strings.Join(env, ", "))
	}

	switch x := any(v).(type) {
	case *time.Duration:
		fSet.DurationVarP(x, name, short, *x, usage)
	case *net.IP:
		fSet.IPVarP(x, name, short, *x, usage)
	case *net.IPNet:
		fSet.IPNetVarP(x, name, short, *x, usage)
	case *string:
		fSet.StringVarP(x, name, short, *x, usage)
	case *int:
		fSet.IntVarP(x, name, short, *x, usage)
	case *int8:
		fSet.Int8VarP(x, name, short, *x, usage)
	case *int16:
		fSet.Int16VarP(x, name, short, *x, usage)
	case *int32:
		fSet.Int32VarP(x, name, short, *x, usage)
	case *int64:
		fSet.Int64VarP(x, name, short, *x, usage)
	case *uint:
		fSet.UintVarP(x, name, short, *x, usage)
	case *uint8:
		fSet.Uint8VarP(x, name, short, *x, usage)
	case *uint16:
		fSet.Uint16VarP(x, name, short, *x, usage)
	case *uint32:
		fSet.Uint32VarP(x, name, short, *x, usage)
	case *uint64:
		fSet.Uint64VarP(x, name, short, *x, usage)
	case *float32:
		fSet.Float32VarP(x, name, short, *x, usage)
	case *float64:
		fSet.Float64VarP(x, name, short, *x, usage)
	case *bool:
		fSet.BoolVarP(x, name, short, *x, usage)
	case *[]time.Duration:
		fSet.DurationSliceVarP(x, name, short, *x, usage)
	case *[]net.IP:
		fSet.IPSliceVarP(x, name, short, *x, usage)
	case *[]net.IPNet:
		fSet.IPNetSliceVarP(x, name, short, *x, usage)
	case *[]string:
		fSet.StringArrayVarP(x, name, short, *x, usage)
	case *[]int:
		fSet.IntSliceVarP(x, name, short, *x, usage)
	case *[]int32:
		fSet.Int32SliceVarP(x, name, short, *x, usage)
	case *[]int64:
		fSet.Int64SliceVarP(x, name, short, *x, usage)
	case *[]uint:
		fSet.UintSliceVarP(x, name, short, *x, usage)
	case *[]float32:
		fSet.Float32SliceVarP(x, name, short, *x, usage)
	case *[]float64:
		fSet.Float64SliceVarP(x, name, short, *x, usage)
	case *[]bool:
		fSet.BoolSliceVarP(x, name, short, *x, usage)
	default:
		panic(fmt.Errorf("%s type %v(%T) not support", name, x, x))
	}

	if s != "" && !reDeprecated.MatchString(usage) {
		// 先解析命令行，仅为未覆盖的参数应用环境值，避免污染切片追加状态。
		f := fSet.Lookup(name)
		f.DefValue = s
		f.Annotations = map[string][]string{"xlp-env": {envName, s}}
	}
}

func Changed(name string) bool {
	return CommandLine.Changed(name)
}

var reDeprecated = regexp.MustCompile(`\s*\*\*(.+)\*\*\s*`)

func ParseFlag(fSet *pflag.FlagSet) (err error) {
	fSet.SortFlags = false

	fSet.VisitAll(func(f *pflag.Flag) {
		if matches := reDeprecated.FindStringSubmatch(f.Usage); len(matches) > 0 {
			f.Usage = reDeprecated.ReplaceAllString(f.Usage, "")
			f.Deprecated = matches[1]
		}
	})

	if err := fSet.Parse(os.Args[1:]); err != nil {
		return err
	}
	fSet.VisitAll(func(f *pflag.Flag) {
		if env := f.Annotations["xlp-env"]; err == nil && len(env) == 2 && !f.Changed {
			if e := f.Value.Set(env[1]); e != nil {
				err = fmt.Errorf("环境变量 %s（--%s）无效：%w", env[0], f.Name, e)
			}
		}
	})
	return err
}

func GetEnv(def string, keys ...string) (s string) {
	if len(keys) > 0 {
		for _, e := range keys {
			if s = os.Getenv(e); s != "" {
				return
			}
		}
	}
	return def
}
