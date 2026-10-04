package rendergraph

import (
	"encoding/json"
	"sort"
)

// PlanExport 是导出的调度与分配计划，用于确定性比对。
type PlanExport struct {
	Levels     []Level                    `json:"levels"`
	Lifetimes  map[ResourceID][2]int      `json:"lifetimes"`
	Allocation map[ResourceID]AllocSlot   `json:"allocation"`
	TotalBytes int                        `json:"totalBytes"`
	Resources  map[ResourceID]ResourceDesc `json:"resources"`
}

// ExportPlan 导出调度顺序与资源分配结果（JSON），用于确定性比对。
// 相同图结构与配置下输出必然一致（所有 map 键序由编码前排序或 JSON 字典序保证）。
func (c *Compiled) ExportPlan() ([]byte, error) {
	exp := PlanExport{
		Levels:     c.Levels,
		Lifetimes:  map[ResourceID][2]int{},
		Allocation: c.Allocation,
		TotalBytes: c.TotalBytes,
		Resources:  map[ResourceID]ResourceDesc{},
	}
	for id := range c.FirstUse {
		exp.Lifetimes[id] = [2]int{c.FirstUse[id], c.LastUse[id]}
	}
	for id, d := range c.resDescs {
		exp.Resources[id] = d
	}
	// encoding/json 对 map 键按字典序输出，Levels 为有序切片，整体确定。
	return json.MarshalIndent(exp, "", "  ")
}

// SortedResourceIDs 返回排序后的资源名（辅助测试）。
func (c *Compiled) SortedResourceIDs() []ResourceID {
	var ids []ResourceID
	for id := range c.resDescs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
