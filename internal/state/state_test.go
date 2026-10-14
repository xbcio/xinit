package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &State{
		Version:       CurrentVersion,
		HashAlgo:      "sha256/normlf-v1",
		EngineVersion: "0.1.0",
		Template: TemplateRef{
			Source:   "git@github.com:xbcio/xkitora.git",
			Kind:     "git",
			Revision: "v0.2.4",
			Commit:   "f7bbd61f00000000000000000000000000000000",
		},
		Vars:         map[string]string{"project_name": "Xdesk", "app_slug": "xdesk"},
		TrustedHooks: map[string]string{},
		Files: map[string]FileState{
			"main.go": {Src: "main.go", Hash: "sha256:abc", Exec: false, Binary: false},
		},
	}
	if err := s.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Template.Commit != s.Template.Commit {
		t.Errorf("Commit = %q", got.Template.Commit)
	}
	if got.Vars["app_slug"] != "xdesk" {
		t.Errorf("Vars = %+v", got.Vars)
	}
	if got.Files["main.go"].Hash != "sha256:abc" {
		t.Errorf("Files = %+v", got.Files)
	}
}

func TestSaveIsDeterministic(t *testing.T) {
	// JSON 的 map 序列化必须稳定，否则每次 update 都会产生无意义的 diff
	dir := t.TempDir()
	s := &State{
		Version: CurrentVersion,
		Vars:    map[string]string{"z": "1", "a": "2", "m": "3"},
		Files: map[string]FileState{
			"z.go": {Src: "z.go", Hash: "h1"},
			"a.go": {Src: "a.go", Hash: "h2"},
		},
	}
	var first string
	for i := 0; i < 10; i++ {
		if err := s.Save(dir); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(Path(dir))
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = string(data)
			continue
		}
		if string(data) != first {
			t.Fatalf("state.json 序列化不稳定:\n%s\n---\n%s", first, data)
		}
	}
}

func TestSaveKeepsPreviousVersion(t *testing.T) {
	// 评审 I-1：Save 写新内容前把现有 state.json 的旧字节保留到
	// state.json.prev（恢复面）；首次写入不得凭空造 .prev。
	dir := t.TempDir()
	first := New("0.1.0")
	first.Vars["k"] = "v1"
	if err := first.Save(dir); err != nil {
		t.Fatalf("Save #1: %v", err)
	}
	if _, err := os.Stat(PrevPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("首次 Save 不应产生 .prev: %v", err)
	}
	before, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}

	second := New("0.1.0")
	second.Vars["k"] = "v2"
	if err := second.Save(dir); err != nil {
		t.Fatalf("Save #2: %v", err)
	}
	prev, err := os.ReadFile(PrevPath(dir))
	if err != nil {
		t.Fatalf("第二次 Save 后应保留 .prev: %v", err)
	}
	if !bytes.Equal(prev, before) {
		t.Errorf(".prev 应逐字节等于上一版:\n%q\n---\n%q", before, prev)
	}
	now, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(now, before) {
		t.Error("主文件应已是新内容，不能与上一版相同")
	}
}

func TestSaveKeepsUnparsablePrevAsBytes(t *testing.T) {
	// 保留判据是「读得动」而非「解得开」：已有 state.json 不可解析时，旧字节
	// 照样进 .prev（adopt 覆盖损坏文件前，这是唯一的原文件留存）。
	dir := t.TempDir()
	p := Path(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	garbage := []byte("{ 不是 JSON\n")
	if err := os.WriteFile(p, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := New("0.1.0").Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	prev, err := os.ReadFile(PrevPath(dir))
	if err != nil {
		t.Fatalf(".prev 应保留不可解析的旧字节: %v", err)
	}
	if !bytes.Equal(prev, garbage) {
		t.Errorf(".prev = %q，want %q", prev, garbage)
	}
	if _, err := Load(dir); err != nil {
		t.Fatalf("主文件应是可解析的新状态: %v", err)
	}
}

func TestSavePrevFailureDoesNotBlockMainWrite(t *testing.T) {
	// best-effort（评审 I-1 明示取舍）：.prev 写失败不阻断主写。注入方式：
	// 把 .prev 做成目录占位——WriteFileAtomic 的 rename 必失败（EISDIR 类）。
	dir := t.TempDir()
	st := New("0.1.0")
	if err := st.Save(dir); err != nil {
		t.Fatalf("Save #1: %v", err)
	}
	if err := os.Mkdir(PrevPath(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	st.Vars["k"] = "v2"
	if err := st.Save(dir); err != nil {
		t.Fatalf("保留 .prev 失败不得阻断主写（best-effort）: %v", err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("主文件应是可解析的新内容: %v", err)
	}
	if got.Vars["k"] != "v2" {
		t.Errorf("Vars = %+v，want k=v2（主文件未被写入）", got.Vars)
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("状态缺失应报错，不能猜")
	}
	if !os.IsNotExist(err) {
		// 允许包装，但错误里要能看出是"不存在"（errors.Is 会逐层解包）
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("应由 os.IsNotExist / fs.ErrNotExist 判定为不存在: %v", err)
		}
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("缺失不属于损坏类，CLI 指引的缺失/损坏/读取错误三分流以此为界: %v", err)
	}
}

func TestLoadHintSplitsMissingAndCorrupt(t *testing.T) {
	// 指引句唯一来源（评审 I-1）：缺失沿用旧句、损坏点出 .prev 人工恢复
	// （不自动回退），两句互斥——不得分叉。
	_, errMissing := Load(t.TempDir())
	if hint := LoadHint(errMissing); !strings.Contains(hint, "这不是 xinit 项目") ||
		strings.Contains(hint, "state.json.prev") {
		t.Errorf("缺失指引 = %q", hint)
	}

	dir := t.TempDir()
	p := Path(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{ 不是 JSON\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errCorrupt := Load(dir)
	hint := LoadHint(errCorrupt)
	if !strings.Contains(hint, "解析失败（文件可能已损坏）") ||
		!strings.Contains(hint, "如存在 .xinit/state.json.prev，可将其复制为 .xinit/state.json 后重试") {
		t.Errorf("损坏指引 = %q", hint)
	}
}

func TestLoadHintReadErrorIsNotMissing(t *testing.T) {
	// R2（备案终裁）：state.json 在但读不动（EISDIR 等）既非缺失也非损坏——
	// 指引不得落回缺失句（误指 init/adopt），也不得说「可能已损坏」。
	// 夹具用「state.json 做成目录」：os.ReadFile 稳定失败于非 notexist，且
	// Linux 容器 root 下同样失败（chmod 000 会被 root 读穿，测试会假绿）。
	// 尾段断言（评审 O-1）锁整句尾段，MV-4 类逃逸（删「文件权限或」）自此红。
	dir := t.TempDir()
	if err := os.MkdirAll(Path(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("夹具前提：state.json 为目录应使 Load 失败")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("夹具前提：EISDIR 不应判为不存在: %v", err)
	}
	hint := LoadHint(err)
	if !strings.Contains(hint, "无法读取 .xinit/state.json") {
		t.Errorf("读取错误指引 = %q，want 含 %q", hint, "无法读取 .xinit/state.json")
	}
	if strings.Contains(hint, "这不是 xinit 项目") {
		t.Errorf("读取错误不得落回缺失句（误指 init/adopt）= %q", hint)
	}
	if strings.Contains(hint, "可能已损坏") {
		t.Errorf("读取错误不得沿用损坏句 = %q", hint)
	}
	if !strings.Contains(hint, "请修复文件权限或路径问题后重试") {
		t.Errorf("读取错误指引尾段 = %q，want 含 %q", hint, "请修复文件权限或路径问题后重试")
	}
}

func TestLoadHintSplitsCorruptKinds(t *testing.T) {
	// O-2（park 清零）：损坏类内再分档——版本过高/算法不符不是「文件坏了」，
	// 用户该做的是升级 xinit（.prev 只是备选），指引不得沿用「可能已损坏」。
	// 真锁：Load 的 version/hash_algo 两处 kind 任一退回 corruptParse，或
	// LoadHint 的 mismatch 分支折叠回 parse 句 ⇒ 本测试红。
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"version-too-new", `{"version": 2}`},                        // decode 通过、version 检查先行触发
		{"hash-algo-mismatch", `{"version": 1, "hash_algo": "md5"}`}, // version 过、算法不符
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := Path(dir)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(tc.payload), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir)
			if err == nil {
				t.Fatalf("夹具前提：payload %s 应使 Load 失败", tc.payload)
			}
			hint := LoadHint(err)
			if !strings.Contains(hint, "可升级 xinit 后重试") {
				t.Errorf("错配指引应给升级指引（payload %s）= %q", tc.payload, hint)
			}
			if strings.Contains(hint, "可能已损坏") {
				t.Errorf("错配不得沿用解析失败文案（payload %s）= %q", tc.payload, hint)
			}
		})
	}
}

func TestErrCorruptIsImpliesAs(t *testing.T) {
	// 不变量（O-2 硬化）：LoadHint 用 errors.As 认损坏（state.go:66-68）；凡
	// Is 为真者 As 必真——今日三构造路径全经 corrupt() 成立；未来若新增不经
	// corrupt() 的 ErrCorrupt 形态，本测试红。
	kindOf := func(t *testing.T, err error, want corruptKind) {
		t.Helper()
		if !errors.Is(err, ErrCorrupt) {
			t.Errorf("errors.Is(err, ErrCorrupt) = false: %v", err)
		}
		var ce corruptError
		if !errors.As(err, &ce) {
			t.Fatalf("errors.As(err, &corruptError) = false —— Is 真而 As 假，等价性破裂: %v", err)
		}
		if ce.kind != want {
			t.Errorf("As 取出的 kind = %v，want %v", ce.kind, want)
		}
	}
	for _, tc := range []struct {
		name    string
		payload string
		kind    corruptKind
	}{
		{"parse", "{ 不是 JSON\n", corruptParse},
		{"version-too-new", `{"version": 2}`, corruptMismatch},
		{"hash-algo-mismatch", `{"version": 1, "hash_algo": "md5"}`, corruptMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := Path(dir)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(tc.payload), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir)
			if err == nil {
				t.Fatalf("夹具前提：payload %s 应使 Load 失败", tc.payload)
			}
			kindOf(t, err, tc.kind)
			// 五调用点同款包装（fmt.Errorf("%w\n%s", err, LoadHint(err))，
			// 如 update/session.go:96）不得破 Is⇒As 等价。
			kindOf(t, fmt.Errorf("%w\n%s", err, LoadHint(err)), tc.kind)
		})
	}

	// 缺失：Is 假 ∧ As 假——缺失不是损坏类，也不携带 corruptError 形态。
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("夹具前提：状态缺失应报错")
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("缺失不应判为 ErrCorrupt: %v", err)
	}
	var ce corruptError
	if errors.As(err, &ce) {
		t.Errorf("缺失不应携带 corruptError: %v", err)
	}
}

func TestLoadCorruptIsErrCorrupt(t *testing.T) {
	// 「文件在但不可用」= 损坏类（评审 I-1）：解析失败与截断都归这里，
	// 且不得被当成缺失。
	dir := t.TempDir()
	p := Path(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"{ 不是 JSON\n", `{"version": 1, "hash_algo": "`} {
		if err := os.WriteFile(p, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(dir)
		if err == nil {
			t.Fatalf("损坏状态必须报错，不能猜（payload %q）", payload)
		}
		if !errors.Is(err, ErrCorrupt) {
			t.Errorf("payload %q 应判为 ErrCorrupt: %v", payload, err)
		}
		if os.IsNotExist(err) {
			t.Errorf("损坏不得被当成缺失（payload %q）: %v", payload, err)
		}
	}
}

func TestPathIsDotXinitStateJSON(t *testing.T) {
	// 落盘位置是跨任务契约：Task 18/19 与 init 的 .gitignore 处理都按
	// "<项目根>/.xinit/state.json" 读写。DirName 是常量，若有人改了它的
	// 值，所有消费方会一致地跟着漂移，只有这条断言拦得住。
	p := Path("/proj")
	if filepath.Dir(p) != filepath.Join("/proj", ".xinit") || filepath.Base(p) != "state.json" {
		t.Errorf("状态文件位置是契约 .xinit/state.json，Path = %q", p)
	}
}

func TestLoadRejectsFutureVersion(t *testing.T) {
	dir := t.TempDir()
	// hash_algo 填**合法值**：否则 Load 会因为 hash_algo 不匹配而报错，
	// 这条测试就变成「版本检查被删掉也会绿」的空转夹具。
	payload := fmt.Sprintf(`{"version": 99, "hash_algo": %q, "files": {}}`, fsutil.HashAlgo)
	p := Path(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("状态版本高于本工具支持时应报错")
	}
	if os.IsNotExist(err) {
		// 前提守卫：文件没写进去时 err 也非 nil，测试会空转通过
		t.Fatalf("前提不成立：%s 未写入，Load 失败原因不是版本检查: %v", p, err)
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("版本过高属损坏类（文件在但不可用，评审 I-1）: %v", err)
	}
}

func TestLoadRejectsHashAlgoMismatchIsErrCorrupt(t *testing.T) {
	dir := t.TempDir()
	payload := `{"version": 1, "hash_algo": "md5", "files": {}}`
	p := Path(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("hash 算法不匹配应报错")
	}
	if os.IsNotExist(err) {
		t.Fatalf("前提不成立：%s 未写入，Load 失败原因不是 hash 检查: %v", p, err)
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("hash 不匹配属损坏类（文件在但不可用，评审 I-1）: %v", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	// 除未知字段外，其余字段全部合法（尤其 hash_algo）：否则 DisallowUnknownFields
	// 被删掉后 Load 仍会因 hash_algo 不匹配报错，测试空转通过。
	payload := fmt.Sprintf(`{"version": 1, "hash_algo": %q, "files": {}, "nope": 1}`, fsutil.HashAlgo)
	p := Path(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("未知字段应报错，避免静默忽略拼写错误")
	}
	if os.IsNotExist(err) {
		// 前提守卫：文件没写进去时 err 也非 nil，测试会空转通过
		t.Fatalf("前提不成立：%s 未写入，Load 失败原因不是未知字段: %v", p, err)
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("未知字段属损坏类（文件在但不可用，评审 I-1）: %v", err)
	}
}

func TestWriteFileAtomicPreservesMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.sh")

	if err := WriteFileAtomic(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("权限 = %v, want 0755 —— CreateTemp 默认是 0600，必须显式 chmod",
			info.Mode().Perm())
	}
}

func TestWriteFileAtomicPreservesReadBits(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := WriteFileAtomic(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("权限 = %v, want 0644（group/other 读权限不能丢）", info.Mode().Perm())
	}
}

func TestWriteFileAtomicReplacesContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := WriteFileAtomic(p, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Errorf("= %q", got)
	}
}

func TestWriteFileAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	for i := 0; i < 5; i++ {
		if err := WriteFileAtomic(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		names := []string{}
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("临时文件未清理: %v", names)
	}
}

func TestWriteFileAtomicCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "deep", "er", "a.txt")
	if err := WriteFileAtomic(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("应自动创建父目录: %v", err)
	}
}

func TestStateJSONShape(t *testing.T) {
	// 字段名是落盘契约，改名会让旧状态无法读取
	s := &State{Version: 1, Files: map[string]FileState{}}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"version"`, `"hash_algo"`, `"engine_version"`, `"template"`, `"files"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("state.json 缺少字段 %s: %s", key, data)
		}
	}
}

func TestFileStateMixedEOLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := New("0.0.0")
	st.Files["a.txt"] = FileState{
		Src: "a.txt", Hash: "sha256/normlf-v1:00", MixedEOL: true,
	}
	data, err := st.marshal()
	if err != nil {
		t.Fatal(err)
	}
	// JSON 键名必须按规范 §4.3 是 mixed_eol
	if !strings.Contains(string(data), `"mixed_eol": true`) {
		t.Fatalf("序列化缺少 mixed_eol 键：\n%s", data)
	}
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Files["a.txt"].MixedEOL {
		t.Error("mixed_eol 未在 Save/Load 往返中保留")
	}
}

func TestLoadStateWithoutMixedEOLField(t *testing.T) {
	// M1 写的旧 state.json 没有 mixed_eol 键：必须解码为零值，不能报错
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{
	  "version": 1,
	  "hash_algo": "sha256/normlf-v1",
	  "engine_version": "0.1.0",
	  "template": {"source": "tpl", "kind": "git", "revision": "v1.0.0", "commit": "abc"},
	  "vars": {},
	  "trusted_hooks": {},
	  "files": {"a.txt": {"src": "a.txt", "hash": "sha256/normlf-v1:00", "exec": false, "binary": false, "generated": false}}
	}`
	if err := os.WriteFile(Path(dir), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("旧状态应能解码: %v", err)
	}
	if got.Files["a.txt"].MixedEOL {
		t.Error("缺字段应为 false")
	}
}
