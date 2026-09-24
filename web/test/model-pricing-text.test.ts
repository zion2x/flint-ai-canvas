import { expect, test } from "bun:test";

import { modelQuoteRequest, priceTierSummaryLabel, priceTiersForCurrentSelection, requestCreditCost, type ModelPriceTier } from "../src/lib/model-pricing";
import { createModelChannel, defaultConfig, type AiConfig } from "../src/stores/use-config-store";

function textPricingConfig(billingMode: "fixed_request" | "token") {
    const tiers: ModelPriceTier[] = [
        { selector: {}, billingMode: "fixed_request", unitPriceMicrocredits: 1_000_000, resolution: "*", videoSeconds: 0, inputTokenPriceMicrocredits: 0, outputTokenPriceMicrocredits: 0, cachedTokenPriceMicrocredits: 0 },
        { selector: { operation: "text_generation" }, billingMode, unitPriceMicrocredits: billingMode === "token" ? 0 : 2_000_000, resolution: "*", videoSeconds: 0, inputTokenPriceMicrocredits: 1_000_000, outputTokenPriceMicrocredits: 2_000_000, cachedTokenPriceMicrocredits: 0 },
    ];
    const channel = createModelChannel({
        id: "system-channel",
        name: "System channel",
        scope: "system",
        models: ["text-model"],
        modelCosts: [{ model: "text-model", capability: "text", pricePolicy: "channel", billingMode: "fixed_request", unitPriceMicrocredits: 1_000_000, logicalModelId: "logical-text", logicalPriceTiers: tiers }],
    });
    const config: AiConfig = { ...defaultConfig, channels: [channel], model: "system-channel::text-model" };
    return { config, channel, tiers: channel.modelCosts![0]!.logicalPriceTiers! };
}

test("prefers the text generation price over the uniform fallback and uses it for quotes", () => {
    const { config, channel, tiers } = textPricingConfig("fixed_request");

    expect(priceTiersForCurrentSelection(tiers, "text", config)).toEqual([tiers[1]!]);
    expect(requestCreditCost({ channelMode: "remote", modelCosts: channel.modelCosts, model: "text-model", capability: "text", config, count: 1 })).toBe(2);
    expect(modelQuoteRequest(config, config.model, "text")).toMatchObject({
        logicalModelID: "logical-text",
        intent: { capability: "text", operation: "text_generation" },
    });
});

test("does not display a fixed cost when the matching text generation tier bills tokens", () => {
    const { config, channel, tiers } = textPricingConfig("token");
    const matched = priceTiersForCurrentSelection(tiers, "text", config);

    expect(matched).toHaveLength(1);
    expect(matched[0]?.billingMode).toBe("token");
    expect(requestCreditCost({ channelMode: "remote", modelCosts: channel.modelCosts, model: "text-model", capability: "text", config })).toBeNull();
    expect(priceTierSummaryLabel(matched, "text")).toBe("输出 2 积分/百万 Token");
});
