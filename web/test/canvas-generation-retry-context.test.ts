import { describe, expect, test } from "bun:test";

import { createGenerationBatchRetryContexts, createGenerationRetryContext, resetInterruptedGeneration } from "@/lib/canvas/canvas-project-generation";
import { resetGenerationTaskMetadata } from "@/lib/canvas/canvas-task-state";
import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";

describe("canvas generation retry context", () => {
    test("keeps the same idempotency key when Web Crypto is unavailable on HTTP", async () => {
        const expected = await createGenerationRetryContext("failed-task", "attempt-group");
        const previousDigest = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", new TextEncoder().encode("generation-retry\0attempt-group\0failed-task")));
        expect(expected.clientOperationId).toBe(`retry:${Array.from(previousDigest, (byte) => byte.toString(16).padStart(2, "0")).join("")}`);
        const descriptor = Object.getOwnPropertyDescriptor(globalThis, "crypto")!;
        try {
            for (const cryptoValue of [{ subtle: undefined }, undefined]) {
                Object.defineProperty(globalThis, "crypto", { configurable: true, value: cryptoValue });
                const retry = await createGenerationRetryContext("failed-task", "attempt-group");
                expect(retry).toEqual(expected);
                expect(retry.clientOperationId).toMatch(/^retry:[a-f0-9]{64}$/);
                expect(await createGenerationRetryContext("different-task", "attempt-group")).not.toEqual(retry);
                expect(await createGenerationRetryContext("failed-task", "different-group")).not.toEqual(retry);

                const batch = await createGenerationBatchRetryContexts(["failed-task", "different-task"], "attempt-group");
                expect(batch[0]).toEqual(retry);
                expect(new Set(batch.map((item) => item.clientOperationId)).size).toBe(2);
            }
        } finally {
            Object.defineProperty(globalThis, "crypto", descriptor);
        }
    });

    test("a failed task can enter retry loading without retaining its terminal display state", async () => {
        const failed = {
            status: "error" as const,
            taskId: "failed-task",
            taskStatus: "failed" as const,
            taskProgress: 0,
            taskStage: "任务失败",
            errorDetails: "Invalid URL",
        };
        const retryContext = await createGenerationRetryContext(failed.taskId);
        const loading = resetGenerationTaskMetadata(failed, "loading");

        expect(retryContext.retryOf).toBe("failed-task");
        expect(loading).toMatchObject({ status: "loading" });
        expect(loading.taskId).toBeUndefined();
        expect(loading.taskStatus).toBeUndefined();
        expect(loading.taskProgress).toBeUndefined();
        expect(loading.taskStage).toBeUndefined();
        expect(loading.errorDetails).toBeUndefined();
    });

    test("restores persisted loading nodes with terminal tasks as errors", () => {
        const node = (id: string, taskStatus: "failed" | "cancelled" | "running"): CanvasNodeData => ({
            id,
            type: CanvasNodeType.Video,
            title: id,
            position: { x: 0, y: 0 },
            width: 320,
            height: 180,
            metadata: { status: "loading", taskId: `${id}-task`, taskStatus, taskProgress: 0, taskStage: "任务失败" },
        });
        const restored = resetInterruptedGeneration([node("failed", "failed"), node("cancelled", "cancelled"), node("running", "running")]);

        expect(restored[0].metadata).toMatchObject({ status: "error", taskId: "failed-task", taskStatus: "failed", taskProgress: 0 });
        expect(restored[1].metadata).toMatchObject({ status: "error", taskId: "cancelled-task", taskStatus: "cancelled" });
        expect(restored[2].metadata).toMatchObject({ status: "loading", taskId: "running-task", taskStatus: "running" });
    });
});
