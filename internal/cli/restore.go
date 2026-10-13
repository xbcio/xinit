package cli

import (
	"sort"
)

// base 机制（快照装载/读树/元数据重建/§5.2 恢复链/恢复行尾注）已随管线迁入
// internal/update/base.go（T2/M1，§2.7 #17）；本文件只留 cli 侧保留的通用
// helper。

// sortedStringKeys 返回 map 键的排序切片（确定性输出）。
// 包内通用 helper，Task 12 起继续复用。
// §2.7 #11：与 internal/update/base.go 的同名泛型副本两处各留一份。
func sortedStringKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
