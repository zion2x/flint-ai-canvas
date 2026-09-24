package app

import (
	"errors"
	"testing"

	"infinite-canvas/backend/internal/model"
)

func TestTextPriceTiersMatchBusinessOperations(t *testing.T) {
	channelModel := model.ChannelModel{PriceTiers: []model.ChannelModelPriceTier{
		{ID: "fallback", SelectorJSON: `{}`, Enabled: true, PriceConfigured: true},
		{ID: "text", SelectorJSON: `{"operation":"text_generation"}`, Enabled: true, PriceConfigured: true},
	}}
	for _, operation := range []string{"", "text", "storyboard", "creative", "cloud_agent_step", "text_generation"} {
		t.Run(operation, func(t *testing.T) {
			intent := ModelRequestIntentFromTaskInput(map[string]any{"mode": "text"}, "canvas_text", operation)
			tier := channelModelPriceTierForIntent(channelModel, intent)
			if tier == nil || tier.ID != "text" {
				t.Fatalf("operation %q matched %#v, want the explicit text_generation tier", operation, tier)
			}
			if intent.Operation != operation {
				t.Fatalf("business operation changed from %q to %q", operation, intent.Operation)
			}
		})
	}
}

func TestTextGenerationTierAdmissionAndQuote(t *testing.T) {
	profile := DefaultModelCapabilityConfigForModel(string(model.ChannelInterfaceOpenAIResponse), "text-model")
	tier := model.ChannelModelPriceTier{
		ID: "text-generation", SelectorKey: `{"operation":"text_generation"}`, SelectorJSON: `{"operation":"text_generation"}`,
		ProviderModelKey: "upstream-text-model", BillingMode: "token", Enabled: true, PriceConfigured: true,
		InputTokenPriceMicrocredits: 10_000_000, OutputTokenPriceMicrocredits: 20_000_000,
	}
	svc, db, channel, channelModel := createSystemChannelSelectionFixture(t, "text", model.ChannelInterfaceOpenAIResponse, profile, []model.ChannelModelPriceTier{tier})
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.BillingOrder{}); err != nil {
		t.Fatal(err)
	}
	baseline, err := svc.QuoteChannelModel(ChannelModelQuoteRequest{ChannelID: channel.ID, ModelKey: channelModel.ModelKey, Intent: ModelRequestIntent{Capability: "text", Operation: "text_generation"}})
	if err != nil || baseline == nil || baseline.AmountMicrocredits <= 0 || baseline.BillingMode != "token" {
		t.Fatalf("canonical text quote = %#v, error = %v", baseline, err)
	}
	for _, operation := range []string{"", "text", "storyboard", "creative", "cloud_agent_step"} {
		t.Run(operation, func(t *testing.T) {
			input, err := svc.resolveSystemChannelModelSelection(map[string]any{
				"mode": "text", "config": map[string]any{"channelId": channel.ID, "model": channelModel.ModelKey},
			}, "canvas_text", operation)
			if err != nil {
				t.Fatalf("text task admission failed: %v", err)
			}
			config := input["config"].(map[string]any)
			if config["priceTierId"] != tier.ID || config["providerModelKey"] != tier.ProviderModelKey {
				t.Fatalf("admission selected an unexpected price tier or upstream model: %#v", config)
			}
			quote, err := svc.QuoteChannelModel(ChannelModelQuoteRequest{ChannelID: channel.ID, ModelKey: channelModel.ModelKey, Intent: ModelRequestIntent{Capability: "text", Operation: operation}})
			if err != nil || quote == nil || *quote != *baseline {
				t.Fatalf("business operation quote = %#v, error = %v, want %#v", quote, err, baseline)
			}
		})
	}
	for _, row := range []any{&model.Task{}, &model.BillingOrder{}} {
		var count int64
		if err := db.Model(row).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("admission/quote wrote %d rows of %T, error = %v", count, row, err)
		}
	}
}

func TestTextGenerationTierRejectsUnavailablePrices(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*model.ChannelModelPriceTier)
	}{
		{name: "disabled", mutate: func(tier *model.ChannelModelPriceTier) { tier.Enabled = false }},
		{name: "unconfigured", mutate: func(tier *model.ChannelModelPriceTier) { tier.PriceConfigured = false }},
		{name: "negative price", mutate: func(tier *model.ChannelModelPriceTier) { tier.InputTokenPriceMicrocredits = -1 }},
		{name: "excessive price", mutate: func(tier *model.ChannelModelPriceTier) {
			tier.OutputTokenPriceMicrocredits = maxChannelModelTokenPriceMicrocredits + 1
		}},
		{name: "different operation", mutate: func(tier *model.ChannelModelPriceTier) { tier.SelectorJSON = `{"operation":"text_to_image"}` }},
	} {
		t.Run(test.name, func(t *testing.T) {
			tier := model.ChannelModelPriceTier{
				ID: "text-generation", SelectorKey: `{"operation":"text_generation"}`, SelectorJSON: `{"operation":"text_generation"}`,
				BillingMode: "token", Enabled: true, PriceConfigured: true, InputTokenPriceMicrocredits: 10_000_000, OutputTokenPriceMicrocredits: 20_000_000,
			}
			test.mutate(&tier)
			profile := DefaultModelCapabilityConfigForModel(string(model.ChannelInterfaceOpenAIResponse), "text-model")
			svc, _, channel, channelModel := createSystemChannelSelectionFixture(t, "text", model.ChannelInterfaceOpenAIResponse, profile, []model.ChannelModelPriceTier{tier})
			_, admissionErr := svc.resolveSystemChannelModelSelection(map[string]any{
				"mode": "text", "config": map[string]any{"channelId": channel.ID, "model": channelModel.ModelKey},
			}, "canvas_text", "text")
			quote, quoteErr := svc.QuoteChannelModel(ChannelModelQuoteRequest{ChannelID: channel.ID, ModelKey: channelModel.ModelKey, Intent: ModelRequestIntent{Capability: "text", Operation: "text"}})
			if quote != nil {
				t.Fatalf("invalid price produced a quote: %#v", quote)
			}
			for _, err := range []error{admissionErr, quoteErr} {
				var modelErr *ModelError
				if !errors.As(err, &modelErr) || modelErr.ErrorCode != ErrCodeModelPriceNotConfigured {
					t.Fatalf("error = %v, want model_price_not_configured", err)
				}
			}
		})
	}
}
