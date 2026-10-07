package metrics

import (
	"fmt"
	"os"
	"testing"
)

// businessAreas 描述本包测试覆盖的业务域；TestMain 会在运行前打印。
var businessAreas = []string{
	"指标注册校验：重复注册/非法名称/非法或重复标签/标签数上限/未知类型/非法直方图桶",
	"计数器语义：单调递增、负数增量拒绝",
	"仪表语义：可增可减、Set/Add/Inc/Dec",
	"直方法语义：累计桶、sum、count、自动 +Inf 边界",
	"基数控制：达上限后拒绝(PolicyReject)或淘汰最旧(PolicyEvictOldest)",
	"序列 TTL：过期序列惰性回收且不进快照",
	"一致快照：并发写入与暴露下的单调性/完整性/深拷贝(-race)",
	"指标删除：Unregister 后句柄失效且快照不再包含",
}

func TestMain(m *testing.M) {
	fmt.Println("========== metrics 包业务覆盖清单 ==========")
	for i, b := range businessAreas {
		fmt.Printf("  %d. %s\n", i+1, b)
	}
	fmt.Println("===========================================")
	os.Exit(m.Run())
}
