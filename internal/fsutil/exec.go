package fsutil

// ExecSupported 报告本平台是否有「可执行位」语义（§22.6 单点谓词）。
// 导出包级**变量** = 钉死的注入接缝（§22.18 #7）：测试覆写为 false 以在
// darwin 上模拟 Windows 档（defer 恢复纪律）。
var ExecSupported = execSupportedOS()
