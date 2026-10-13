package update

// 来源：internal/update/update.go 的 Run 块 1-6（M2-P0 prepare 抽取；I-2 实施批
// T4/M2a）。阶段判据（§3-M2）：函数体与源区间逐字对应，差异仅「参数接入」与
// 「返回值包装」两类（逐条见 task-4-report.md）。

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// Interactive 是 update 的交互接缝（§2.4；M1 落地为第 ⑥ 类登记变化）：
// cli 的 promptSession 实现它。「唯一 bufio.Reader」与「EOF 锁存」的纪律
// 仍由 promptSession 独占——管线拿不到 reader；Out() 是冲突表头/EOF 行
// （conflicts.go）的唯一打印通道。nil = 本次运行不可交互（cli 侧由
// interactiveSession 判定后注入）。
type Interactive interface {
	ConfirmTrust() (bool, error)
	AskConflict(q string) (yes, eof bool, err error)
	Out() io.Writer
}

// session 是 P0 建立的跨阶段上下文（§2.2 P0 产物行；dryRun 为控制器裁定
// R-M2-1，§2.2 字段列表缺口）。io/now 的消费者在 M4（Input.IO/Input.Now）；
// M2 期间只初始化、不读取。
type session struct {
	dir           string           // = abs(in.ProjectDir)
	st            *state.State     // = state.Load(dir)（块 3）
	lock          *state.Lock      // 块 5；nil = dry-run（连 lock 文件都不建）
	cacheRoot     string           // 块 6 = template.DefaultCacheRoot()
	io            IO               // M2 无消费者；M4 接 Input.IO
	interactive   Interactive      // = in.Interactive（nil = 不可交互）
	now           func() time.Time // M2 无消费者；M4 接 Input.Now
	mode          Mode             // = in.Mode
	dryRun        bool             // = in.DryRun（R-M2-1）
	noHooks       bool             // = in.NoHooks
	engineVersion string           // = in.EngineVersion（P10 消费，v2-I-2）
}

// prepare 是 P0（§2.2）：入口守卫（块 1）、路径归一 + 交互会话（块 2）、
// 读 state（块 3）、git 跟踪守卫（块 4）、项目锁（块 5）、缓存根（块 6）。
// 报告头两行与 rep.Source/OldRevision/OldCommit 依 R-M2-3 留守 Run
// （值经 s.st），故本函数不触碰 Report。
func prepare(in Input) (*session, error) {
	if len(in.Accept) > 0 && in.DryRun {
		return nil, fmt.Errorf("--accept 与 --dry-run 不能组合：accept 的本义是修改状态（§10.3）")
	}
	projectDir, err := filepath.Abs(in.ProjectDir)
	if err != nil {
		return nil, err
	}

	// 交互会话（§19.2.2/§19.2.3）：cli 入口创建一次（创建只做一次 ioctl
	// 判定——不读 stdin、不打印），经 Input.Interactive 注入；nil = 不可交互，
	// 调用点一律跳过提问（--dry-run 下不提问，故不会碰 stdin）。
	s := &session{
		dir:           projectDir,
		interactive:   in.Interactive,
		io:            IO{Out: os.Stdout, Err: os.Stderr},
		now:           time.Now,
		mode:          in.Mode,
		dryRun:        in.DryRun,
		noHooks:       in.NoHooks,
		engineVersion: in.EngineVersion,
	}

	// 先 Load 再取锁：Load 失败（不是 xinit 项目）时不该凭空创建 .xinit/。
	st, err := state.Load(projectDir)
	if err != nil {
		// 缺失/损坏两条指引同 status/check（评审 I-1：单一来源，不得分叉）。
		return nil, fmt.Errorf("%w\n%s", err, state.LoadHint(err))
	}
	s.st = st

	// 写前安全检查（§5.5，与 init 同一判据）。
	if tracked, err := state.IsGitTracked(projectDir, state.DirName); err != nil {
		return nil, fmt.Errorf("无法确认 %s/ 是否被 git 跟踪: %w", state.DirName, err)
	} else if tracked {
		return nil, fmt.Errorf(
			"%s/ 已被 git 跟踪：拒绝写入被版本控制的 %s/（规范 §5.5）；请执行 `git rm -r --cached %s` 后重试",
			state.DirName, state.DirName, state.DirName)
	}

	// 1. 项目锁（R12：--dry-run 只读，不加锁——连 lock 文件都不创建）
	if !in.DryRun {
		lock, err := state.LockProject(projectDir)
		if err != nil {
			return nil, err
		}
		s.lock = lock // 原 defer lock.Unlock() 移交 s.close()（R-M2-7）
	}

	cacheRoot, err := template.DefaultCacheRoot()
	if err != nil {
		s.close() // R-M2-7：块 6 失败时已获锁必须释放（与原 defer 语义等价）
		return nil, err
	}
	s.cacheRoot = cacheRoot
	return s, nil
}

// close 释放 P0 获取的资源——原 Run 的 defer lock.Unlock()（I29：Run 结束时
// 解锁）。dry-run 下 lock = nil，no-op。
func (s *session) close() {
	if s.lock != nil {
		s.lock.Unlock()
	}
}
