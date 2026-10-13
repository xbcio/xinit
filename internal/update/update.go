package update

// 来源：internal/cli/update.go 行 92-1283（迁移集：runUpdate + 全部消费方辅助
// 函数闭包；I-2 实施批 T2/M1）。本文件承载 Input/IO/Mode/Verdict 接缝声明 +
// verdictOf 单点 + Run 编排骨架（§2.6 M2 布局：update.go = 包文档/Input/IO/
// Mode/Verdict/Run）。阶段函数与 helper 归位（M2a/T4、M2b/T5、M2c/T6 收口）：
// session.go（P0）、template.go（P1）、base.go（P2）、ours.go（P3）、
// accept.go（P4/P6）、decide.go（P5 + classifyMerges/buildChanges/
// execConclusion）、gate.go（P7）、trust.go（P8）、deliver.go（P9）、
// advance.go（P10）、hook.go（P11）、report.go（终局 report() 单点组装）。

import "io"

// Mode 是管线入口模式（§2.4：报告头等可观测字符串的单一来源）。
type Mode int

const (
	ModeUpdate Mode = iota // xinit update
	ModeDiff               // xinit diff（只读镜像；DryRun 恒为真）
)

// Verdict 是终局语义（§2.3），cli 用一张 5 行表映射为 §11.5 数字。
type Verdict int

const (
	VerdictOK        Verdict = iota // → 0
	VerdictPending                  // → 2（冲突/折叠拒绝/障碍物）
	VerdictHookFail                 // → 3
	VerdictNeedTrust                // → 4（零写）
	VerdictChanged                  // → 5（dry-run 预测有非 Noop）
)

// verdictOf 是终局语义的单点（§11.5、§2.3、I27：优先级 3 ＞ 2 ＞ 0；M1 起
// 只产 Verdict，cli 单点映射为码）：冲突/折叠拒绝/障碍物待解决 → 2；
// 钩子失败 → 3（本轮没有完整做完；2 的状态已持久化，下一轮照旧会报）。
func verdictOf(pending, hookFailed bool) Verdict {
	if hookFailed {
		return VerdictHookFail
	}
	if pending {
		return VerdictPending
	}
	return VerdictOK
}

// IO 是管线输出通道组（§2.6 update.go 行；M2 期间无消费者——M4 接
// Input.IO。P0 的 session.io 初始化 = os.Stdout/os.Stderr 对）。
type IO struct {
	Out io.Writer
	Err io.Writer
}

// Input 是 update 的全部输入（cli 侧由 flag 经 inputOf 映射，§2.7 #3）。
// M1 只落「搬运 + 编译必需的最小接缝」：IO/Now 留 M4（§2.4 交互行）。
type Input struct {
	ProjectDir    string
	Revision      string
	ConfigPath    string
	Fetch         bool
	Force         bool
	DryRun        bool
	NoHooks       bool
	Yes           bool
	Accept        []string
	Mode          Mode        // ModeDiff = diff（报告头派生；M1 起不再由 cli 覆盖 Header）
	Interactive   Interactive // nil = 不可交互（cli 侧 interactiveSession 判定）
	EngineVersion string      // §2.7 #13：cli 为唯一真源，经此注入（M2 落 session）
}

// Run 是 §10.2 的十二步本体（不打印；diff 与 cli 测试复用它）。
// 返回的报告总是非 nil；退出码见 §11.5。
func Run(in Input) (*Report, error) {
	s, err := prepare(in)
	if err != nil {
		return nil, err
	}
	defer s.close() // 解锁（I29）；dry-run 下 lock = nil 为 no-op

	t, err := loadTemplate(s, in.Revision, in.ConfigPath, in.Fetch)
	if err != nil {
		return nil, err
	}
	b, err := loadBase(s)
	if err != nil {
		return nil, err
	}
	o, err := snapshotOurs(s, t, b)
	if err != nil {
		return nil, err
	}
	a, err := rewriteAccept(s, b, o, in.Accept)
	if err != nil {
		return nil, err
	}
	d, err := buildDecision(s, t, b, o, a, hooksRun(s, t))
	if err != nil {
		return nil, err
	}
	if err := refineDecision(s, t, b, o, d, a); err != nil {
		return nil, err
	}
	g, v, done, err := gate(s, t, b, o, d, a)
	if err != nil {
		return nil, err
	}
	if done {
		return report(s, t, b, o, d, g, nil, v, nil), nil // 终局①：pending / dry-run
	}
	if done, err := trust(s, t, in.NoHooks, in.Yes); err != nil {
		return nil, err
	} else if done {
		return report(s, t, b, o, d, g, nil, VerdictNeedTrust, nil), nil // 终局②：rc4（零写）
	}
	w, err := deliver(s, b, o, d, g)
	if err != nil {
		return nil, err
	}
	if err := advance(s, t, b, d, w); err != nil {
		return nil, err
	}
	h, err := runHook(s, t, d.Actions, hooksRun(s, t))
	if err != nil {
		return nil, err
	}
	return report(s, t, b, o, d, g, w, verdictOf(w.Pending, h.Failed), h), nil // 终局③
}
