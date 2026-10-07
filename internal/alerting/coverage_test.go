package alerting

import (
	"fmt"
	"os"
	"testing"
)

// businessAreas 描述本包测试覆盖的业务域；TestMain 会在运行前打印。
var businessAreas = []string{
	"规则校验：名称/指标/操作符/窗口/For 时长约束，失败原因可区分",
	"规则生命周期：注册、重复注册、删除、初始无数据状态",
	"操作符与标签选择器：六种比较与精确匹配路由",
	"状态机迁移：无数据→待触发→已触发→恢复→回落 的完整路径",
	"无数据恢复：样本滑出窗口按无数据判定，不沿用旧值",
	"重复与乱序：重复时间戳不重复触发、迟到点不丢恢复",
	"注册表集成：快照喂入 + 注册表 TTL 与引擎窗口联动",
	"并发：并发喂样本与并发评估，触发计数恰好一次(-race)",
}

func TestMain(m *testing.M) {
	fmt.Println("========== alerting 包业务覆盖清单 ==========")
	for i, b := range businessAreas {
		fmt.Printf("  %d. %s\n", i+1, b)
	}
	fmt.Println("============================================")
	os.Exit(m.Run())
}
