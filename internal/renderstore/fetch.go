package renderstore

import (
	"os"
	"path/filepath"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/state"
)

// Fetch 尝试从快照取回 need 中的路径（§21.4 校验链）。
// 缓存不存在/不可读/清单坏/任何校验不符 → 对应路径**静默**视为未命中；
// 返回仅含通过全部校验的 Dst → 当轮渲染原文（全 miss 时返回 nil）。
// 只读：只用 os.ReadFile/os.Open，**绝不创建任何目录/文件**。
func Fetch(st *state.State, cacheRoot string, need []string) map[string][]byte {
	if len(need) == 0 {
		return nil
	}

	key := familyKey(st.EngineVersion, st.Template.Source, st.Template.Kind, st.Vars)
	dir := entryDir(cacheRoot, key)
	m, err := readManifest(filepath.Join(dir, manifestName))
	if err != nil {
		return nil // 缓存不存在/不可读/清单坏：整体静默 miss
	}

	// 整体门（键已保证这些分量，作 belt）：任一不符 → 整体 miss。
	if m.Version != manifestVersion ||
		m.Engine != st.EngineVersion ||
		m.Source != st.Template.Source ||
		m.Kind != st.Template.Kind ||
		m.Anchor != st.Template.Commit ||
		m.VarsSHA256 != varsHash(st.Vars) {
		return nil
	}

	byDst := make(map[string]fileEntry, len(m.Files))
	for _, f := range m.Files {
		byDst[f.Dst] = f
	}

	var out map[string][]byte
	for _, dst := range need {
		f, ok := byDst[dst]
		if !ok {
			continue // 该路径不在清单
		}
		fst, ok := st.Files[dst]
		if !ok {
			continue // need 由 st.Files 驱动；理论不达，保守 miss
		}
		// 安全门：坏 Dst（绝对路径 / .. 段）按未命中处理——不读 tree/、
		// 不写回、不得硬错（§21.6/§21.9）。
		if err := state.CheckWithinBase(dst); err != nil {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, treeDirName, filepath.FromSlash(dst)))
		if err != nil {
			continue
		}
		// raw 门先行：tree/ 必须逐字节等于写入时原文（CRLF/LF 孪生归一化后
		// 同 hash，只有 raw 门能拦下）。
		if sha256Hex(content) != f.RawSHA256 {
			continue
		}
		// 记录门：逐路径归一化比对必须保留（update_test.go:339 金丝雀）。
		if fsutil.CompareHash(content, fst.Binary) != fst.Hash {
			continue
		}
		if out == nil {
			out = make(map[string][]byte)
		}
		out[dst] = content
	}
	return out
}
