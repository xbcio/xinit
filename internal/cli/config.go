package cli

import (
	"fmt"
	"os"
	"sort"

	"github.com/goccy/go-yaml"
)

// LoadConfig 读一个扁平的 key-value 变量文件。
//
// 值必须是字符串：YAML 里写 feature: false 会被 goccy 静默转成字符串
// "false"，而它在 {{ if .feature }} 里是真值。类型系统拦不住，必须
// 显式查（与 manifest 的同名检查同一个理由）。
func LoadConfig(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s: %w", path, err)
	}

	var raw map[string]any
	if err := yaml.UnmarshalWithOptions(data, &raw, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("解析配置 %s: %w", path, err)
	}

	out := make(map[string]string, len(raw))
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := raw[k]
		if v == nil {
			out[k] = ""
			continue
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf(
				"%s: 变量 %q 的值必须是字符串，实际是 %T (%v)", path, k, v, v)
		}
		out[k] = s
	}
	return out, nil
}
