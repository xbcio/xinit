package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xbcio/xinit/internal/fsutil"
)

// CurrentVersion 是 state.json 的格式版本。
const CurrentVersion = 1

// DirName 是状态目录名。
const DirName = ".xinit"

// State 是 .xinit/state.json 的全部内容。
type State struct {
	Version       int                  `json:"version"`
	HashAlgo      string               `json:"hash_algo"`
	EngineVersion string               `json:"engine_version"`
	Template      TemplateRef          `json:"template"`
	Vars          map[string]string    `json:"vars"`
	TrustedHooks  map[string]string    `json:"trusted_hooks"`
	Files         map[string]FileState `json:"files"`
}

// TemplateRef 记录 base 对应的模板版本。
type TemplateRef struct {
	Source   string `json:"source"`
	Kind     string `json:"kind"`     // "git" | "dir"
	Revision string `json:"revision"` // 用户给的引用名
	Commit   string `json:"commit"`   // git：commit sha；dir：树 hash
}

// FileState 是一个受管路径的基线记录。
//
// Hash 是 **base 的 hash**，不是磁盘现状 —— 判断"本地是否改过"只需拿
// 磁盘 hash 与之比较，不必重新渲染。
type FileState struct {
	Src       string `json:"src"` // 模板原路径，重建 base 时反查用
	Hash      string `json:"hash"`
	Exec      bool   `json:"exec"`
	Binary    bool   `json:"binary"`
	Generated bool   `json:"generated"` // 上次更新时该路径是否属于 generated
}

// New 构造一个空状态。
func New(engineVersion string) *State {
	return &State{
		Version:       CurrentVersion,
		HashAlgo:      fsutil.HashAlgo,
		EngineVersion: engineVersion,
		Vars:          map[string]string{},
		TrustedHooks:  map[string]string{},
		Files:         map[string]FileState{},
	}
}

// Path 返回状态文件在给定项目根下的位置。
func Path(projectDir string) string {
	return filepath.Join(projectDir, DirName, "state.json")
}

// Load 读取状态。
//
// 状态缺失或版本不认识时**立刻失败并说明**，绝不猜 —— 猜出来的 base
// 会让三方合并静默产生错误结果。
func Load(projectDir string) (*State, error) {
	p := Path(projectDir)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var s State
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", p, err)
	}
	if s.Version > CurrentVersion {
		return nil, fmt.Errorf(
			"%s 的版本是 %d，高于本工具支持的 %d；请升级 xinit",
			p, s.Version, CurrentVersion)
	}
	if s.HashAlgo != fsutil.HashAlgo {
		return nil, fmt.Errorf(
			"%s 的 hash 算法是 %q，本工具用 %q；状态与本版本不兼容",
			p, s.HashAlgo, fsutil.HashAlgo)
	}
	return &s, nil
}

// Save 原子地写状态。
//
// 输出是确定性的：map 键按字节序排序后写出。否则每次 update 都会产生
// 无意义的 diff（文件本身也一样，只是内容变了）。
func (s *State) Save(projectDir string) error {
	data, err := s.marshal()
	if err != nil {
		return err
	}
	return WriteFileAtomic(Path(projectDir), data, 0o644)
}

func (s *State) marshal() ([]byte, error) {
	// encoding/json 序列化 map 时按键的字节序排序，所以输出是确定的。
	// 这个性质由 TestSaveIsDeterministic 守着：将来若换成自定义 encoder，
	// 必须自己保证同样的确定性，否则每次 update 都产生无意义的 diff。
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, fmt.Errorf("序列化状态: %w", err)
	}
	return buf.Bytes(), nil
}
