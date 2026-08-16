package mcpcanvas

import "testing"

func TestAgentToNativeMultilineLabelFitsContainer(t *testing.T) {
	shape, label := agentToNative(map[string]interface{}{
		"id":       "failure",
		"type":     "rectangle",
		"x":        100,
		"y":        100,
		"width":    160,
		"height":   60,
		"fontSize": 16,
		"text":     "订单失败处理\n退避重试 -> DLQ / 人工补偿",
	})
	if label == nil {
		t.Fatal("多行形状标签未生成")
	}
	_, textH := textMetrics("订单失败处理\n退避重试 -> DLQ / 人工补偿", 16)
	labelH, _ := num(label["height"])
	if labelH != textH {
		t.Fatalf("标签高度 = %v，期望 %v", labelH, textH)
	}
	shapeW, _ := num(shape["width"])
	labelW, _ := num(label["width"])
	if shapeW < labelW+32 {
		t.Fatalf("容器宽度 = %v，无法容纳标签宽度 %v", shapeW, labelW)
	}
}

func TestResolveArrowBindingsWritesGeometryAndRepositionsLabel(t *testing.T) {
	start, _ := agentToNative(map[string]interface{}{
		"id": "start", "type": "rectangle", "x": 0, "y": 0, "width": 160, "height": 80,
	})
	end, _ := agentToNative(map[string]interface{}{
		"id": "end", "type": "rectangle", "x": 400, "y": 0, "width": 160, "height": 80,
	})
	arrow, label := agentToNative(map[string]interface{}{
		"id": "flow", "type": "arrow", "startElementId": "start", "endElementId": "end", "text": "次数耗尽",
	})
	native := []map[string]interface{}{start, end, arrow, label}
	resolveArrowBindings(native)

	width, _ := num(arrow["width"])
	if width <= 0 {
		t.Fatalf("箭头 width = %v，期望正数", width)
	}
	labelX, _ := num(label["x"])
	if labelX < 180 || labelX > 360 {
		t.Fatalf("箭头标签 x = %v，未落在最终路径中点附近", labelX)
	}
}
