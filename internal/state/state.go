package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xbcio/xinit/internal/fsutil"
)

// CurrentVersion 是 state.json 的格式版本。
const CurrentVersion = 1

// DirName 是状态目录名。
const DirName = ".xinit"

// ErrCorrupt 标记「state.json 在磁盘上，但不可用」这一类载入失败：解析失败、
// 未知字段、版本高于本工具、hash 算法不匹配（均属「文件在但不可用」）。
// 与「文件缺失」（fs.ErrNotExist）相对——CLI 据此在两条指引间分流（评审 I-1）。
//
// 它只是分类标记：不进错误消息（消息逐字是契约），用 errors.Is 判。
var ErrCorrupt = errors.New("state.json 在磁盘上但无法使用")

// corruptError 把 Load 的「文件在但不可用」类失败标记为 ErrCorrupt：Is 认
// 哨兵，Error() 只透出底层原文——错误文案是逐字契约（测试与用户指引都按
// 原文对接），分类标记不进消息。
type corruptError struct{ error }

func (corruptError) Is(target error) bool { return target == ErrCorrupt }

// corrupt 给 err 打「损坏」标记（nil 直通）。
func corrupt(err error) error {
	if err == nil {
		return nil
	}
	return corruptError{err}
}

// LoadHint 返回 Load 失败时追加给用户看的指引句——CLI 各调用点唯一来源，
// 不得分叉：缺失 → init/adopt 指引（既有文案逐字）；损坏（ErrCorrupt）→
// .prev 人工恢复指引。
//
// 「如存在 .xinit/state.json.prev」不查盘（简单优先）：句子自带条件，不做 IO。
func LoadHint(err error) string {
	if errors.Is(err, ErrCorrupt) {
		return "state.json 解析失败（文件可能已损坏）；如存在 .xinit/state.json.prev，可将其复制为 .xinit/state.json 后重试"
	}
	return "这不是 xinit 项目，或 .xinit/state.json 缺失；请从模板 init 生成，或用 xinit adopt 重建基线（§5.3）"
}

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
	MixedEOL  bool   `json:"mixed_eol"` // §9.1：DetectEOL == EOLMixed；行 6 的第二判据
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

// PrevPath 返回上一版状态文件（state.json.prev）的位置：Save 写新内容前
// 把现有 state.json 的**旧字节**保留到这里。
//
// Load 不读它——恢复是**人工复制**（评审 I-1：不引入自动回退）。
func PrevPath(projectDir string) string {
	return filepath.Join(projectDir, DirName, "state.json.prev")
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
		return nil, corrupt(fmt.Errorf("解析 %s: %w", p, err))
	}
	if s.Version > CurrentVersion {
		return nil, corrupt(fmt.Errorf(
			"%s 的版本是 %d，高于本工具支持的 %d；请升级 xinit",
			p, s.Version, CurrentVersion))
	}
	if s.HashAlgo != fsutil.HashAlgo {
		return nil, corrupt(fmt.Errorf(
			"%s 的 hash 算法是 %q，本工具用 %q；状态与本版本不兼容",
			p, s.HashAlgo, fsutil.HashAlgo))
	}
	return &s, nil
}

// Save 原子地写状态。
//
// 输出是确定性的：map 键按字节序排序后写出。否则每次 update 都会产生
// 无意义的 diff（文件本身也一样，只是内容变了）。
//
// 写新内容前先把现有 state.json 的**旧字节**（内容不一定可解析，字节为准）
// 保留一份到 PrevPath——这是 state.json 损坏时的恢复面（评审 I-1）。
// 保留失败**不阻断主写**（best-effort）：.prev 是附加保险，写不出它不该
// 拖垮本轮保存；主写的错误照旧返回、不吞。首次写入（无现有文件）不产生
// .prev。
//
// 两份都走 WriteFileAtomic：任何时刻都不出现半写文件。Load 不读 .prev、
// 不自动回退——恢复 = 人工复制。
func (s *State) Save(projectDir string) error {
	data, err := s.marshal()
	if err != nil {
		return err
	}
	p := Path(projectDir)
	if old, rerr := os.ReadFile(p); rerr == nil {
		// best-effort（评审 I-1 明示取舍）：保留失败只影响恢复面，忽略。
		_ = WriteFileAtomic(PrevPath(projectDir), old, 0o644)
	}
	return WriteFileAtomic(p, data, 0o644)
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
