package app

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Compile before quotation and reservation; the worker executes this persisted
// prompt so template changes cannot silently change an approved generation.
func (s *Service) prepareStoryboardTaskInput(userID string, input map[string]any, fallbackPrompt string) error {
	if input["mode"] != "text" || input["agentRequests"] != nil || isTextReplayTaskRequest(input) || taskInputUsesWorkflowProvider(input) {
		return BadAuthRequest("分镜任务必须使用后端文本生成模式")
	}
	duration, err := storyboardIntegerOption(input["shotDurationSeconds"], "单镜头时长", 60)
	if err != nil {
		return err
	}
	count, err := storyboardIntegerOption(input["shotCount"], "镜头数量", 100)
	if err != nil {
		return err
	}
	durationRule, countRule := "按叙事需要安排单镜头时长，每镜 1 到 60 秒。", "按剧情完整性决定镜头数量，至少生成 1 个镜头。"
	if duration > 0 {
		durationRule = fmt.Sprintf("每个镜头固定为 %d 秒。", duration)
	}
	if count > 0 {
		countRule = fmt.Sprintf("必须生成 %d 个镜头，完整覆盖剧情。", count)
	}
	values := map[string]string{
		"剧情":      firstNonEmpty(strings.TrimSpace(stringValue(input["prompt"])), fallbackPrompt),
		"用户要求":    strings.TrimSpace(stringValue(input["requirements"])),
		"单镜头时长规则": durationRule, "镜头数量规则": countRule,
	}
	for key, source := range map[string]string{"项目画风": "projectStyle", "角色版本": "characters", "画布资产": "canvasAssets"} {
		encoded, err := json.Marshal(input[source])
		if err != nil {
			return BadAuthRequest("分镜上下文格式无效：" + key)
		}
		values[key] = string(encoded)
	}
	compiled, err := s.compilePrompt(userID, promptOperationStoryboardPlan, values)
	if err != nil {
		return fmt.Errorf("编译分镜模板失败：%w", err)
	}
	metadata, _ := input["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["promptTemplateOperation"] = promptOperationStoryboardPlan
	metadata["promptTemplateId"] = compiled.TemplateID
	metadata["promptTemplateVersion"] = compiled.TemplateVersion
	delete(metadata, "promptTemplateVariables")
	input["metadata"], input["prompt"] = metadata, compiled.Content
	return nil
}

func storyboardIntegerOption(raw any, label string, maximum int) (int, error) {
	if raw == nil || raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(fmt.Sprint(raw))
	if err != nil || value < 0 || (maximum > 0 && value > maximum) {
		return 0, BadAuthRequest("分镜" + label + "必须是有效的非负整数")
	}
	return value, nil
}

// The model contract is storyboard-plan/v3 (shots). The canvas contract is
// rows: project once here rather than asking models to satisfy two schemas.
func storyboardTaskResult(rawInput string, result map[string]any) (map[string]any, error) {
	text, err := extractPreferredJSONText(stringValue(result["text"]), "shots")
	if err != nil {
		return nil, fmt.Errorf("分镜结果不符合结构契约：%w", err)
	}
	var plan map[string]any
	if err := json.Unmarshal([]byte(text), &plan); err != nil || plan == nil {
		return nil, BadAuthRequest("分镜结果必须是包含 shots 的 JSON 对象")
	}
	if err := storyboardObjectFields(plan, []string{"title", "logline", "styleGuide", "characters", "locations", "shots"}); err != nil {
		return nil, BadAuthRequest("分镜结果：" + err.Error())
	}
	for _, key := range []string{"title", "logline", "styleGuide"} {
		if _, ok := plan[key].(string); !ok {
			return nil, BadAuthRequest("分镜结果缺少有效文本字段 " + key)
		}
	}
	for _, key := range []string{"characters", "locations"} {
		if !storyboardStringArray(plan[key]) {
			return nil, BadAuthRequest("分镜结果缺少有效数组字段 " + key)
		}
	}
	if len([]rune(stringValue(plan["styleGuide"]))) > 120 {
		return nil, BadAuthRequest("分镜结果 styleGuide 最多 120 个字符")
	}
	shots, ok := plan["shots"].([]any)
	if !ok || len(shots) == 0 || len(shots) > 100 {
		return nil, BadAuthRequest("分镜结果必须包含 1 到 100 项的 shots 数组")
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(rawInput), &input); err != nil {
		return nil, fmt.Errorf("分镜任务上下文无法解析：%w", err)
	}
	duration, err := storyboardIntegerOption(input["shotDurationSeconds"], "单镜头时长", 60)
	if err != nil {
		return nil, err
	}
	count, err := storyboardIntegerOption(input["shotCount"], "镜头数量", 100)
	if err != nil {
		return nil, err
	}
	if count > 0 && len(shots) != count {
		return nil, BadAuthRequest(fmt.Sprintf("分镜结果应有 %d 个镜头，实际返回 %d 个", count, len(shots)))
	}
	characterRefs, assetIDs := map[string]map[string]any{}, map[string]bool{}
	for _, value := range interfaceSlice(input["characters"]) {
		if character, ok := value.(map[string]any); ok {
			assetID, name := stringValue(character["assetId"]), stringValue(character["name"])
			if assetID != "" && name != "" {
				characterRefs[assetID] = map[string]any{"characterName": name, "characterAssetId": assetID, "characterVersionId": stringValue(character["versionId"])}
			}
		}
	}
	for _, value := range interfaceSlice(input["canvasAssets"]) {
		if asset, ok := value.(map[string]any); ok {
			assetIDs[stringValue(asset["id"])] = true
		}
	}
	rows := make([]any, 0, len(shots))
	for index, raw := range shots {
		shot, ok := raw.(map[string]any)
		if !ok {
			return nil, BadAuthRequest(fmt.Sprintf("分镜第 %d 个镜头必须是对象", index+1))
		}
		if err := validateStoryboardShot(shot, duration, assetIDs); err != nil {
			return nil, BadAuthRequest(fmt.Sprintf("分镜第 %d 个镜头：%v", index+1, err))
		}
		row := map[string]any{"shotNumber": index + 1}
		for key, value := range shot {
			mapped := map[string]string{"description": "plotDescription", "visualPrompt": "imageGenerationPrompt", "videoPrompt": "videoMotionPrompt", "assetRefs": "assetBindings"}[key]
			if key == "characterIds" {
				characters := make([]any, 0, len(value.([]any)))
				for _, character := range value.([]any) {
					id := character.(string)
					if ref, ok := characterRefs[id]; ok {
						characters = append(characters, ref)
					} else {
						characters = append(characters, map[string]any{"characterName": id})
					}
				}
				row["characters"] = characters
			} else if mapped != "" {
				row[mapped] = value
			} else {
				row[key] = value
			}
		}
		rows = append(rows, row)
	}
	payload := map[string]any{"title": plan["title"], "rows": rows}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	result["mode"], result["title"], result["rows"], result["text"] = "text", plan["title"], rows, string(encoded)
	return result, nil
}

func storyboardStringArray(value any) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return true
}

func validateStoryboardShot(shot map[string]any, requestedDuration int, assetIDs map[string]bool) error {
	textFields := []string{"title", "description", "dialogue", "narrativeIntent", "viewerPOV", "performanceBlocking", "shotSize", "emotion", "lightingAndAtmosphere", "audioEffects", "visualPrompt", "videoPrompt", "camera", "motion", "timeBeats", "continuityOut", "negativePrompt"}
	if err := storyboardObjectFields(shot, append(textFields, "durationSeconds", "characterIds", "mustHave", "optionalDetails", "assetRefs")); err != nil {
		return err
	}
	for _, key := range textFields {
		if _, ok := shot[key].(string); !ok {
			return fmt.Errorf("缺少有效文本字段 %s", key)
		}
	}
	for _, key := range []string{"description", "visualPrompt", "videoPrompt"} {
		if strings.TrimSpace(stringValue(shot[key])) == "" {
			return fmt.Errorf("%s 不能为空", key)
		}
	}
	for _, key := range []string{"characterIds", "mustHave", "optionalDetails"} {
		if !storyboardStringArray(shot[key]) {
			return fmt.Errorf("缺少有效数组字段 %s", key)
		}
	}
	if len(shot["mustHave"].([]any)) > 3 {
		return fmt.Errorf("mustHave 最多 3 项")
	}
	duration, ok := shot["durationSeconds"].(float64)
	if !ok || duration < 1 || duration > 60 || duration != float64(int(duration)) {
		return fmt.Errorf("durationSeconds 必须是 1 到 60 的整数")
	}
	if requestedDuration > 0 && int(duration) != requestedDuration {
		return fmt.Errorf("durationSeconds 必须为指定的 %d 秒", requestedDuration)
	}
	refs, ok := shot["assetRefs"].([]any)
	if !ok || len(refs) > 6 {
		return fmt.Errorf("assetRefs 必须是最多 6 项的数组")
	}
	roles := map[string]bool{"character": true, "environment": true, "wardrobe": true, "prop": true, "weapon": true, "style": true, "motion": true, "audio": true}
	for _, value := range refs {
		ref, ok := value.(map[string]any)
		if !ok || !assetIDs[stringValue(ref["nodeId"])] || strings.TrimSpace(stringValue(ref["nodeId"])) == "" {
			return fmt.Errorf("assetRefs 引用了当前画布资产中不存在的 nodeId")
		}
		if err := storyboardObjectFields(ref, []string{"nodeId", "role", "priority"}); err != nil {
			return err
		}
		priority, ok := ref["priority"].(float64)
		if !roles[stringValue(ref["role"])] || !ok || priority < 0 || priority > 100 || priority != float64(int(priority)) {
			return fmt.Errorf("assetRefs 的 role 或 priority 无效")
		}
	}
	return nil
}

func storyboardObjectFields(object map[string]any, fields []string) error {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	for field := range object {
		if !allowed[field] {
			return fmt.Errorf("包含契约未声明的字段 %s", field)
		}
	}
	return nil
}
