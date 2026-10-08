// Package render 把「模板树 + vars + 清单」渲染成派生项目的内容。
//
// 纯函数核心：不写文件系统，输入是 io/fs.FS，可被穷举测试。
package render

// predicateWindowCap 是谓词回溯窗口的字节上限。
//
// 超限时谓词一律判为**不命中**，绝不截断后匹配 —— 截断会让 '^' 锚定的
// 谓词匹配到截断点，产生假阳性。实测 "abcmodule" + 4090 个空格 + "xkitora"
// 配 '^module[ \t]+' 在截断语义下发生了替换，而 module 并不在行首。
//
// 4096 是实测出来的必要值：真实模板（xkitora 的
// docs/macos-27-reference.md）存在超过 4096 字节的行。
const predicateWindowCap = 4096
