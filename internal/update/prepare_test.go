package update

// T7b/M3b C3 断言：恢复推进域（I29/I30）。落点 internal/update（§4.3 规则 1）。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xbcio/xinit/internal/state"
)

// TestPrepareLocksAfterLoad 钉 I29/I30（session.go「先 Load 再取锁」）：
//
//	I29——非项目目录（state.Load 失败）→ prepare 报错，且 .xinit/ **不存在**
//	     （取锁会 MkdirAll(.xinit)，故这是「Load 先于取锁」的顺序证据）；
//	I30——正常项目 → prepare 成功并持有锁（.xinit/lock 出现），close 后可再取。
func TestPrepareLocksAfterLoad(t *testing.T) {
	t.Run("Load 失败不建 .xinit（先 Load 再取锁）", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := prepare(Input{ProjectDir: dir}); err == nil {
			t.Fatal("非项目目录应报错")
		}
		if _, err := os.Stat(filepath.Join(dir, state.DirName)); !os.IsNotExist(err) {
			t.Errorf("Load 失败时不得凭空创建 .xinit/（取锁在其后）：stat err = %v", err)
		}
	})

	t.Run("正常项目持锁，close 后可再取", func(t *testing.T) {
		isolateCacheT(t)
		dir := projectWithState(t, state.TemplateRef{Source: "/tpl", Kind: "dir"},
			map[string]state.FileState{"a.txt": textState("a.txt", "x\n")})
		s, err := prepare(Input{ProjectDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, state.DirName, "lock")); err != nil {
			t.Errorf("prepare 应已取锁（lock 文件存在）：%v", err)
		}
		s.close()
		l, err := state.LockProject(dir)
		if err != nil {
			t.Errorf("close 后应能再次取锁（锁已释放）：%v", err)
		} else {
			l.Unlock()
		}
	})
}
