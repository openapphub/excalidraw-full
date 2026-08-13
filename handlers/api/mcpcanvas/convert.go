package mcpcanvas

import (
	"math"
	"math/rand"
	"time"
)

// Format conversion between the mcp-excalidraw "agent format" (text/label/
// start/end, no Excalidraw internals) and the native Excalidraw element
// format the host app's `canvases` table stores (seed/versionNonce/
// boundElements/startBinding/endBinding/...).

// agentToNative converts one agent-format element into a native Excalidraw
// element. Bound text (label/text on shapes and arrows) becomes a separate
// text element with containerId + boundElements wiring, mirroring what the
// mcp reference server's expandElementsForExport does. The generated text
// element is returned as a second value; the caller appends it to the
// element list right after its parent.
func agentToNative(el map[string]interface{}) (map[string]interface{}, map[string]interface{}) {
	typ, _ := el["type"].(string)
	base := make(map[string]interface{}, 24)
	for k, v := range el {
		switch k {
		case "createdAt", "updatedAt", "syncedAt", "source", "syncTimestamp", "label", "start", "end", "text", "version":
			// server-only / agent-only fields stripped
		default:
			base[k] = v
		}
	}
	// Excalidraw defaults
	if _, ok := base["angle"]; !ok {
		base["angle"] = 0
	}
	if _, ok := base["strokeColor"]; !ok {
		base["strokeColor"] = "#1e1e1e"
	}
	if _, ok := base["backgroundColor"]; !ok {
		base["backgroundColor"] = "transparent"
	}
	if _, ok := base["fillStyle"]; !ok {
		base["fillStyle"] = "solid"
	}
	if _, ok := base["strokeWidth"]; !ok {
		base["strokeWidth"] = 2
	}
	if _, ok := base["strokeStyle"]; !ok {
		base["strokeStyle"] = "solid"
	}
	if _, ok := base["roughness"]; !ok {
		// Official Excalidraw default is 0 (smooth, non-hand-drawn). The
		// mcp reference server used 1 (hand-drawn); match the official
		// look so AI-drawn shapes/arrows render like the editor defaults.
		base["roughness"] = 0
	}
	if _, ok := base["opacity"]; !ok {
		base["opacity"] = 100
	}
	if _, ok := base["groupIds"]; !ok {
		base["groupIds"] = []interface{}{}
	}
	if _, ok := base["frameId"]; !ok {
		base["frameId"] = nil
	}
	if _, ok := base["roundness"]; !ok {
		if typ == "rectangle" || typ == "diamond" || typ == "ellipse" {
			base["roundness"] = map[string]interface{}{"type": 3}
		} else {
			base["roundness"] = nil
		}
	}
	if _, ok := base["seed"]; !ok {
		base["seed"] = rand.Intn(2147483647)
	}
	if _, ok := base["versionNonce"]; !ok {
		base["versionNonce"] = rand.Intn(2147483647)
	}
	if _, ok := base["isDeleted"]; !ok {
		base["isDeleted"] = false
	}
	if _, ok := base["boundElements"]; !ok {
		base["boundElements"] = nil
	}
	if _, ok := base["updated"]; !ok {
		base["updated"] = time.Now().UnixMilli()
	}
	if _, ok := base["link"]; !ok {
		base["link"] = nil
	}
	if _, ok := base["locked"]; !ok {
		base["locked"] = false
	}
	if _, ok := base["index"]; !ok {
		base["index"] = "a0"
	}
	if _, ok := base["version"]; !ok {
		base["version"] = 1
	}

	// Arrow / line: resolve start/end refs into bindings. Accept both the
	// mcp CLI shape ({start:{id}, end:{id}}) and the raw agent shape
	// ({startElementId, endElementId}) so curl-direct callers work too.
	if typ == "arrow" || typ == "line" {
		if _, ok := base["points"]; !ok {
			base["points"] = [][]float64{{0, 0}, {100, 0}}
		}
		if _, ok := base["lastCommittedPoint"]; !ok {
			base["lastCommittedPoint"] = nil
		}
		start, _ := el["start"].(map[string]interface{})
		end, _ := el["end"].(map[string]interface{})
		startID, _ := start["id"].(string)
		endID, _ := end["id"].(string)
		if startID == "" {
			startID, _ = el["startElementId"].(string)
		}
		if endID == "" {
			endID, _ = el["endElementId"].(string)
		}
		if startID != "" {
			base["startBinding"] = map[string]interface{}{"elementId": startID, "focus": 0, "gap": 4, "fixedPoint": nil}
		} else if _, ok := base["startBinding"]; !ok {
			base["startBinding"] = nil
		}
		if endID != "" {
			base["endBinding"] = map[string]interface{}{"elementId": endID, "focus": 0, "gap": 4, "fixedPoint": nil}
		} else if _, ok := base["endBinding"]; !ok {
			base["endBinding"] = nil
		}
		if _, ok := base["startArrowhead"]; !ok {
			base["startArrowhead"] = nil
		}
		if _, ok := base["endArrowhead"]; !ok {
			if typ == "arrow" {
				base["endArrowhead"] = "arrow"
			} else {
				base["endArrowhead"] = nil
			}
		}
		if _, ok := base["elbowed"]; !ok {
			base["elbowed"] = false
		}
	}

	// Standalone text: keep text fields
	if typ == "text" {
		text, _ := el["text"].(string)
		base["text"] = text
		base["originalText"] = text
		if _, ok := base["fontSize"]; !ok {
			base["fontSize"] = 20
		}
		if _, ok := base["fontFamily"]; !ok {
			base["fontFamily"] = 1
		}
		if _, ok := base["textAlign"]; !ok {
			base["textAlign"] = "center"
		}
		if _, ok := base["verticalAlign"]; !ok {
			base["verticalAlign"] = "middle"
		}
		if _, ok := base["autoResize"]; !ok {
			base["autoResize"] = true
		}
		if _, ok := base["lineHeight"]; !ok {
			base["lineHeight"] = 1.25
		}
		if _, ok := base["containerId"]; !ok {
			base["containerId"] = nil
		}
		// Estimate width/height if missing (agent-created text often has none)
		if w, ok := num(base["width"]); !ok || w == 0 {
			lines := 1
			longest := 0
			cur := 0
			for _, c := range text {
				if c == '\n' {
					lines++
					if cur > longest {
						longest = cur
					}
					cur = 0
					continue
				}
				cur++
			}
			if cur > longest {
				longest = cur
			}
			fs, _ := num(base["fontSize"])
			if fs == 0 {
				fs = 20
			}
			base["width"] = math.Ceil(float64(longest)*fs*0.6)
			base["height"] = math.Ceil(float64(lines)*fs*1.25)
		}
		return base, nil
	}

	// Shape with label/text: create bound text element
	labelText := ""
	if label, ok := el["label"].(map[string]interface{}); ok {
		labelText, _ = label["text"].(string)
	}
	if labelText == "" {
		labelText, _ = el["text"].(string)
	}
	if labelText == "" {
		return base, nil
	}
	id, _ := el["id"].(string)
	textID := id + "-label"
	// Add binding reference to parent
	be, _ := base["boundElements"].([]interface{})
	base["boundElements"] = append(be, map[string]interface{}{"type": "text", "id": textID})
	// Compute text position
	var textX, textY, textW, textH float64
	if typ == "arrow" || typ == "line" {
		pts, _ := base["points"].([][]float64)
		lastX, lastY := 100.0, 0.0
		if len(pts) > 0 {
			last := pts[len(pts)-1]
			lastX, lastY = last[0], last[1]
		}
		midX, _ := num(base["x"])
		midY, _ := num(base["y"])
		labelW := math.Max(float64(len(labelText))*10, 60)
		textX = midX + lastX/2 - labelW/2
		textY = midY + lastY/2 - 12
		textW = labelW
		textH = 24
	} else {
		x, _ := num(base["x"])
		y, _ := num(base["y"])
		w, _ := num(base["width"])
		h, _ := num(base["height"])
		if w == 0 {
			w = 160
		}
		if h == 0 {
			h = 80
		}
		textX = x + 10
		textY = y + h/4
		textW = w - 20
		textH = h / 2
	}
	fs := 16.0
	if typ == "arrow" || typ == "line" {
		fs = 14
	}
	if f, ok := num(base["fontSize"]); ok && f > 0 {
		fs = f
	}
	textEl := map[string]interface{}{
		"id": textID, "type": "text",
		"x": textX, "y": textY, "width": textW, "height": textH,
		"angle": 0, "strokeColor": "#1e1e1e", "backgroundColor": "transparent",
		"fillStyle": "solid", "strokeWidth": 1, "strokeStyle": "solid",
		"roughness": 0, "opacity": 100, "groupIds": []interface{}{},
		"frameId": nil, "index": "a1", "roundness": nil,
		"seed": rand.Intn(2147483647), "version": 1,
		"versionNonce": rand.Intn(2147483647), "isDeleted": false,
		"boundElements": nil, "updated": time.Now().UnixMilli(),
		"link": nil, "locked": false,
		"text": labelText, "originalText": labelText,
		"fontSize": fs, "fontFamily": 1,
		"textAlign": "center", "verticalAlign": "middle",
		"autoResize": true, "lineHeight": 1.25,
		"containerId": id,
	}
	return base, textEl
}

// resolveArrowBindings computes edge-to-edge paths for every arrow/line in
// the native element list, mirroring the mcp reference server's algorithm:
// the arrow starts at the source shape's edge (toward the target center) and
// ends at the target shape's edge (toward the source center), with a small
// gap. Shapes are looked up by id across the whole list, so arrows resolve
// even when their endpoints were created in the same batch.
func resolveArrowBindings(native []map[string]interface{}) {
	byID := map[string]map[string]interface{}{}
	for _, el := range native {
		if id, ok := el["id"].(string); ok {
			byID[id] = el
		}
	}
	for _, el := range native {
		typ, _ := el["type"].(string)
		if typ != "arrow" && typ != "line" {
			continue
		}
		startID := ""
		if sb, ok := el["startBinding"].(map[string]interface{}); ok {
			startID, _ = sb["elementId"].(string)
		}
		endID := ""
		if eb, ok := el["endBinding"].(map[string]interface{}); ok {
			endID, _ = eb["elementId"].(string)
		}
		if startID == "" && endID == "" {
			continue
		}
		startEl := byID[startID]
		endEl := byID[endID]
		startCenter := centerOf(el)
		endCenter := centerOf(el)
		if startEl != nil {
			startCenter = centerOf(startEl)
		}
		if endEl != nil {
			endCenter = centerOf(endEl)
		}
		startPt := startCenter
		if startEl != nil {
			startPt = edgePoint(startEl, endCenter)
		}
		endPt := endCenter
		if endEl != nil {
			endPt = edgePoint(endEl, startCenter)
		}
		// Apply gap: move start away from source, end away from target
		const gap = 8.0
		dx := endPt[0] - startPt[0]
		dy := endPt[1] - startPt[1]
		dist := math.Sqrt(dx*dx+dy*dy)
		if dist == 0 {
			dist = 1
		}
		finalStart := [2]float64{startPt[0] + dx/dist*gap, startPt[1] + dy/dist*gap}
		finalEnd := [2]float64{endPt[0] - dx/dist*gap, endPt[1] - dy/dist*gap}
		el["x"] = finalStart[0]
		el["y"] = finalStart[1]
		el["points"] = [][]float64{{0, 0}, {finalEnd[0] - finalStart[0], finalEnd[1] - finalStart[1]}}
	}
}

func centerOf(el map[string]interface{}) [2]float64 {
	x, _ := num(el["x"])
	y, _ := num(el["y"])
	w, _ := num(el["width"])
	h, _ := num(el["height"])
	return [2]float64{x + w/2, y + h/2}
}

// edgePoint computes the intersection of the ray from the element's center
// toward targetCenter with the element's edge (rectangle/ellipse/diamond
// geometry), mirroring the reference server's computeEdgePoint.
func edgePoint(el map[string]interface{}, target [2]float64) [2]float64 {
	typ, _ := el["type"].(string)
	cx, _ := num(el["x"])
	cy, _ := num(el["y"])
	w, _ := num(el["width"])
	h, _ := num(el["height"])
	hw := w / 2
	hh := h / 2
	dx := target[0] - (cx + hw)
	dy := target[1] - (cy + hh)
	if dx == 0 && dy == 0 {
		return [2]float64{cx + hw, cy + hh + hh}
	}
	switch typ {
	case "diamond":
		scale := 1.0
		denom := math.Abs(dx)/hw + math.Abs(dy)/hh
		if denom > 0 {
			scale = 1 / denom
		}
		return [2]float64{cx + hw + dx*scale, cy + hh + dy*scale}
	case "ellipse":
		angle := math.Atan2(dy, dx)
		return [2]float64{cx + hw + hw*math.Cos(angle), cy + hh + hh*math.Sin(angle)}
	default: // rectangle
		angle := math.Atan2(dy, dx)
		tanA := math.Tan(angle)
		if math.Abs(tanA*hw) <= hh {
			signX := 1.0
			if dx < 0 {
				signX = -1
			}
			return [2]float64{cx + hw + signX*hw, cy + hh + signX*hw*tanA}
		}
		signY := 1.0
		if dy < 0 {
			signY = -1
		}
		return [2]float64{cx + hw + signY*hh/tanA, cy + hh + signY*hh}
	}
}

// nativeToAgent converts a native Excalidraw element back to agent format:
// strips Excalidraw internals, converts startBinding/endBinding back to
// start/end refs. Bound text elements are folded back into their parent's
// `label` by the caller (load), which has the full element list.
func nativeToAgent(el map[string]interface{}) map[string]interface{} {	typ, _ := el["type"].(string)
	agent := make(map[string]interface{}, 16)
	for k, v := range el {
		switch k {
		case "seed", "versionNonce", "isDeleted", "updated", "index", "lastCommittedPoint",
			"elbowed", "baseline", "autoResize", "lineHeight", "containerId",
			"originalText", "textAlign", "verticalAlign", "fontFamily", "fontSize",
			"startBinding", "endBinding", "boundElements", "frameId", "roundness",
			"groupIds", "link", "locked", "angle", "fillStyle", "strokeStyle",
			"strokeWidth", "roughness", "opacity", "version":
			// Excalidraw internals stripped
		default:
			agent[k] = v
		}
	}
	// Arrow bindings -> start/end refs
	if typ == "arrow" || typ == "line" {
		if sb, ok := el["startBinding"].(map[string]interface{}); ok {
			if id, _ := sb["elementId"].(string); id != "" {
				agent["start"] = map[string]interface{}{"id": id}
			}
		}
		if eb, ok := el["endBinding"].(map[string]interface{}); ok {
			if id, _ := eb["elementId"].(string); id != "" {
				agent["end"] = map[string]interface{}{"id": id}
			}
		}
	}
	return agent
}
