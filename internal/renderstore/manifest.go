package renderstore

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// manifestVersion 是清单格式版本（§21.4；读侧 version != 1 → miss）。
const manifestVersion = 1

// fileEntry 是清单里的单条受管路径记录（§21.4）。
type fileEntry struct {
	Dst       string `json:"dst"`
	RawSHA256 string `json:"raw_sha256"`
	Size      int    `json:"size"`
}

// manifest 是 render/<familyKey>/manifest.json 的结构（有效令牌，§10.2 哲学：
// 最后写、先于读被信任）。不存时间戳——确定性友好、免无谓改写。
type manifest struct {
	Version    int         `json:"version"`
	Engine     string      `json:"engine"`
	Source     string      `json:"source"`
	Kind       string      `json:"kind"`
	Anchor     string      `json:"anchor"`
	VarsSHA256 string      `json:"vars_sha256"`
	Files      []fileEntry `json:"files"`
}

// readManifest 读取并解析清单；任何失败（含文件不存在）都返回 error。
func readManifest(path string) (*manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析快照清单 %s: %w", path, err)
	}
	return &m, nil
}

// encode 输出确定性 JSON：字段序固定（结构体声明序）、files 按 dst 字节序、
// MarshalIndent 两空格 + 尾换行；不修改接收者。
func (m *manifest) encode() ([]byte, error) {
	files := make([]fileEntry, len(m.Files))
	copy(files, m.Files)
	sort.Slice(files, func(i, j int) bool { return files[i].Dst < files[j].Dst })

	out := *m
	out.Files = files
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化快照清单: %w", err)
	}
	return append(data, '\n'), nil
}
