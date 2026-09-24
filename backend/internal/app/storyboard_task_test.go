package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/platform"
)

func storyboardTaskRequest() CreateTaskRequest {
	req := creationTextRequest()
	req.Operation, req.Prompt = "storyboard", "6s"
	req.Input["prompt"] = "6s"
	req.Input["requirements"] = "保留人物动作"
	req.Input["projectStyle"] = map[string]any{"title": "水墨", "prompt": "黑白水墨"}
	req.Input["characters"] = []any{map[string]any{"assetId": "hero", "name": "阿青", "versionId": "hero-v1"}}
	req.Input["canvasAssets"] = []any{map[string]any{"id": "tree", "title": "古树", "type": "image"}}
	req.Input["shotDurationSeconds"], req.Input["shotCount"] = 6, 1
	req.Input["textOptions"] = map[string]any{"stream": false}
	return req
}

func TestStoryboardTaskAdmissionAndCreationQuoteUseCompiledTemplate(t *testing.T) {
	s, db, runID, guard := creationTestService(t)
	if err := db.Model(&model.ChannelModelPriceTier{}).Where("id = ?", "tier").Updates(map[string]any{"billing_mode": "token", "input_token_price_microcredits": 1000000, "output_token_price_microcredits": 1000000}).Error; err != nil {
		t.Fatal(err)
	}
	item, err := s.PrepareCreationSubmission("user", runID, CreationRequest{CreationGuard: guard, ItemKey: "storyboard", Request: storyboardTaskRequest()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproveCreationSubmissions("user", runID, CreationRequest{CreationGuard: guard, SubmissionIDs: []string{item.ID}}); err != nil {
		t.Fatal(err)
	}
	task, err := s.ExecuteCreationSubmission("user", runID, CreationRequest{CreationGuard: guard, SubmissionID: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.repo.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err = json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	prompt := stringValue(input["prompt"])
	for _, want := range []string{"storyboard-plan/v3", "6s", "保留人物动作", "黑白水墨", "阿青", "古树", "6 秒", "1 个镜头"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("compiled prompt is missing %q", want)
		}
	}
	order, err := s.repo.BillingOrder(task.BillingOrderID)
	if err != nil {
		t.Fatal(err)
	}
	estimate := estimateTaskTokens(input)
	if item.Quote.AmountMicrocredits != order.AmountMicrocredits || order.AmountMicrocredits != estimate.InputTokens+estimate.OutputTokens {
		t.Fatalf("quote, reservation and compiled input diverged: quote=%d order=%d estimate=%+v", item.Quote.AmountMicrocredits, order.AmountMicrocredits, estimate)
	}
}

func TestStoryboardTaskRejectsUnstructuredProviderText(t *testing.T) {
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	s, db, _, _ := creationTestService(t)
	s.coordinator = platform.NewCoordinatorWithRedis(nil, "storyboard-test")
	s.platform = nil
	s.activeCancels = make(map[string]context.CancelFunc)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		encoded, _ := json.Marshal(body)
		if !strings.Contains(string(encoded), "storyboard-plan/v3") {
			t.Error("provider did not receive the protected storyboard template")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"What would you like me to do with 6s?"}}],"usage":{"prompt_tokens":20,"completion_tokens":10}}`))
	}))
	defer server.Close()
	if err := db.Model(&model.ModelChannel{}).Where("id = ?", "channel").Updates(map[string]any{"base_url": server.URL, "api_key": "test-key"}).Error; err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("user", storyboardTaskRequest())
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.repo.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = s.taskWorker().processNextTask()
	if err == nil || !strings.Contains(err.Error(), "分镜") {
		t.Fatalf("unstructured storyboard output was accepted: %v", err)
	}
	if !s.BillingFailureRequiresReview(task.BillingOrderID, task.ID, err) {
		t.Fatal("successful upstream usage must not be automatically refunded after a storyboard contract failure")
	}
	stored, _ := s.repo.Task(task.ID)
	order, _ := s.repo.BillingOrder(task.BillingOrderID)
	account, _ := s.repo.CreditAccount("user")
	var refunded int64
	db.Model(&model.CreditLedgerEntry{}).Where("billing_order_id = ? AND type IN ?", order.ID, []string{"refund", "consume"}).Count(&refunded)
	if stored.Status != model.TaskStatusFailed || order.Status != model.BillingStatusUncertain || account.ReservedMicrocredits != order.AmountMicrocredits || refunded != 0 || calls.Load() != 1 {
		t.Fatalf("invalid contract terminal state: task=%s billing=%s reserved=%d settlement entries=%d calls=%d", stored.Status, order.Status, account.ReservedMicrocredits, refunded, calls.Load())
	}
}

func validStoryboardPlan() map[string]any {
	shot := map[string]any{
		"title": "树下相逢", "description": "阿青停在古树下", "durationSeconds": 6,
		"visualPrompt": "水墨古树下的阿青", "videoPrompt": "阿青抬头，镜头缓缓推进",
		"characterIds": []any{"hero"}, "mustHave": []any{"古树"}, "optionalDetails": []any{},
		"assetRefs": []any{map[string]any{"nodeId": "tree", "role": "environment", "priority": 80}},
	}
	for _, field := range []string{"dialogue", "narrativeIntent", "viewerPOV", "performanceBlocking", "shotSize", "emotion", "lightingAndAtmosphere", "audioEffects", "camera", "motion", "timeBeats", "continuityOut", "negativePrompt"} {
		shot[field] = ""
	}
	return map[string]any{"title": "相逢", "logline": "树下相逢", "styleGuide": "水墨", "characters": []any{"阿青"}, "locations": []any{"古树"}, "shots": []any{shot}}
}

func TestStoryboardTaskProjectsCanonicalShotsToCanvasRows(t *testing.T) {
	input, _ := json.Marshal(storyboardTaskRequest().Input)
	plan, _ := json.Marshal(validStoryboardPlan())
	result, err := storyboardTaskResult(string(input), map[string]any{"text": string(plan), "reasoning": "kept"})
	if err != nil {
		t.Fatal(err)
	}
	rows := result["rows"].([]any)
	row := rows[0].(map[string]any)
	character := row["characters"].([]any)[0].(map[string]any)
	if row["plotDescription"] != "阿青停在古树下" || row["imageGenerationPrompt"] != "水墨古树下的阿青" || row["videoMotionPrompt"] != "阿青抬头，镜头缓缓推进" || character["characterName"] != "阿青" || character["characterAssetId"] != "hero" || character["characterVersionId"] != "hero-v1" || row["assetBindings"].([]any)[0].(map[string]any)["nodeId"] != "tree" || result["reasoning"] != "kept" {
		t.Fatalf("storyboard projection lost generation fields: %#v", result)
	}
	var replay map[string]any
	if err = json.Unmarshal([]byte(result["text"].(string)), &replay); err != nil || len(replay["rows"].([]any)) != 1 || replay["shots"] != nil {
		t.Fatalf("terminal text and rows use different contracts: %#v, %v", replay, err)
	}
}

func TestStoryboardTaskRejectsInvalidShots(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any, map[string]any)
	}{
		{"empty shots", func(p, _ map[string]any) { p["shots"] = []any{} }},
		{"rows is not the model schema", func(p, _ map[string]any) { p["rows"] = p["shots"]; delete(p, "shots") }},
		{"missing prompt", func(_, s map[string]any) { delete(s, "visualPrompt") }},
		{"empty description", func(_, s map[string]any) { s["description"] = " " }},
		{"empty motion prompt", func(_, s map[string]any) { s["videoPrompt"] = "" }},
		{"fractional duration", func(_, s map[string]any) { s["durationSeconds"] = 6.5 }},
		{"wrong duration", func(_, s map[string]any) { s["durationSeconds"] = 8 }},
		{"wrong count", func(p, s map[string]any) { p["shots"] = []any{s, s} }},
		{"unknown asset", func(_, s map[string]any) { s["assetRefs"].([]any)[0].(map[string]any)["nodeId"] = "invented" }},
		{"unexpected bindings", func(_, s map[string]any) { s["assetBindings"] = []any{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := validStoryboardPlan()
			tc.mutate(plan, plan["shots"].([]any)[0].(map[string]any))
			encoded, _ := json.Marshal(plan)
			input, _ := json.Marshal(storyboardTaskRequest().Input)
			if _, err := storyboardTaskResult(string(input), map[string]any{"text": string(encoded)}); err == nil || !strings.Contains(err.Error(), "分镜") {
				t.Fatalf("invalid storyboard accepted: %v", err)
			}
		})
	}
}

func TestStoryboardTaskCreationRejectsChangedTemplate(t *testing.T) {
	s, db, runID, guard := creationTestService(t)
	template := model.PromptTemplate{ID: "storyboard-template", Operation: promptOperationStoryboardPlan, Name: "测试", Version: 1, Content: "模板AAAA", OutputType: "json", Enabled: true}
	if err := db.Create(&template).Error; err != nil {
		t.Fatal(err)
	}
	item, err := s.PrepareCreationSubmission("user", runID, CreationRequest{CreationGuard: guard, ItemKey: "storyboard", Request: storyboardTaskRequest()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproveCreationSubmissions("user", runID, CreationRequest{CreationGuard: guard, SubmissionIDs: []string{item.ID}}); err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&model.PromptTemplate{}).Where("id = ?", template.ID).Update("content", "模板BBBB").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExecuteCreationSubmission("user", runID, CreationRequest{CreationGuard: guard, SubmissionID: item.ID}); err == nil || !strings.Contains(err.Error(), "报价已变化") {
		t.Fatalf("equal-price template change used previous approval: %v", err)
	}
	var taskCount int64
	db.Model(&model.Task{}).Count(&taskCount)
	if taskCount != 0 {
		t.Fatalf("changed template created %d tasks", taskCount)
	}
}

func TestStoryboardTaskExecutesCompiledSnapshot(t *testing.T) {
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	s, db, _, _ := creationTestService(t)
	s.coordinator = platform.NewCoordinatorWithRedis(nil, "storyboard-test")
	s.platform = nil
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		encoded, _ := json.Marshal(body)
		if !strings.Contains(string(encoded), "模板AAAA") || strings.Contains(string(encoded), "模板BBBB") {
			t.Error("worker recompiled an approved storyboard prompt")
		}
		plan, _ := json.Marshal(validStoryboardPlan())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(plan)}}}})
	}))
	defer server.Close()
	if err := db.Model(&model.ModelChannel{}).Where("id = ?", "channel").Updates(map[string]any{"base_url": server.URL, "api_key": "test-key"}).Error; err != nil {
		t.Fatal(err)
	}
	template := model.PromptTemplate{ID: "storyboard-template", Operation: promptOperationStoryboardPlan, Name: "测试", Version: 1, Content: "模板AAAA", OutputType: "json", Enabled: true}
	if err := db.Create(&template).Error; err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("user", storyboardTaskRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&model.PromptTemplate{}).Where("id = ?", template.ID).Update("content", "模板BBBB").Error; err != nil {
		t.Fatal(err)
	}
	task, err = s.repo.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := s.processTask(context.Background(), *task)
	if err != nil || len(result["rows"].([]any)) != 1 {
		t.Fatalf("persisted storyboard snapshot failed: %#v, %v", result, err)
	}
}
