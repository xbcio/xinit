# golden 夹具

每个子目录的布局：

    <case>/
      template/      模板树（xinit.yml 在这里）
      vars.yml       变量（扁平 key-value，值必须是字符串）
      expected.hash  期望的渲染树 hash（由 UPDATE_GOLDEN=1 生成）

模板**必须**放在 `template/` 子目录里。`vars.yml` 与 `expected.hash`
本身也是文件，直接拿 `<case>/` 当模板根会把它们一并渲染进 golden，
等于把测试元数据混进了被测输出。

## 为什么 expected.hash 不含 exec 位

Windows 上读不出执行位，`tar` 解包也会丢。把 exec 编进跨平台 golden
会让测试在 CI 上无故失败。exec 由 `TestGoldenExecBit` 单独断言。

## 更新期望值

改动渲染行为后：

    UPDATE_GOLDEN=1 go test ./internal/render -run TestGoldenTrees

**生成之后必须逐字节核对预期输出再提交** —— 无脑更新会让 golden 失去
意义。
