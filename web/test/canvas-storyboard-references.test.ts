import { describe, expect, test } from "bun:test";

import { expandStoryboardTextMentions } from "../src/lib/canvas/canvas-project-domain";
import { buildNodeMentionReferences, type CanvasResourceReference } from "../src/lib/canvas/canvas-resource-references";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData } from "../src/types/canvas";

describe("expandStoryboardTextMentions", () => {
    test("展开引用控件保存的 canonical node token", () => {
        const references: CanvasResourceReference[] = [{
            id: "script-node",
            nodeId: "script-node",
            kind: "text",
            label: "文本1",
            title: "完整剧本",
            text: "第一场：主角推开仓库大门。",
            active: true,
        }];

        expect(expandStoryboardTextMentions("@[node:script-node] 拆成 8 个镜头", references)).toBe(
            "【项目设定：完整剧本】\n第一场：主角推开仓库大门。 拆成 8 个镜头",
        );
    });

    test("只有时长要求时也传入已连接的画风和完整剧情", () => {
        const node = (id: string, type: CanvasNodeType, title: string, content = ""): CanvasNodeData => ({
            id, type, title, position: { x: 0, y: 0 }, width: 320, height: 180, metadata: { content },
        });
        const script = node("script", CanvasNodeType.Script, "分镜脚本");
        const nodes = [
            script,
            node("style", CanvasNodeType.Text, "项目画风", "电影写实画风。"),
            node("story", CanvasNodeType.Text, "故事梗概", "第一场：主角推开仓库大门。\n第二场：发现失踪的同伴。"),
            node("unconnected", CanvasNodeType.Text, "其他故事", "不应进入本次分镜。"),
        ];
        const connections: CanvasConnection[] = [
            { id: "style-input", fromNodeId: "style", toNodeId: script.id, toHandleId: "storyboard:context" },
            { id: "story-input", fromNodeId: "story", toNodeId: script.id, toHandleId: "storyboard:context" },
            { id: "story-duplicate", fromNodeId: "story", toNodeId: script.id, toHandleId: "storyboard:context" },
        ];

        expect(expandStoryboardTextMentions("6s", buildNodeMentionReferences(script, nodes, connections))).toBe(
            "6s\n\n【项目设定：项目画风】\n电影写实画风。\n\n【项目设定：故事梗概】\n第一场：主角推开仓库大门。\n第二场：发现失踪的同伴。",
        );
    });

    test("已显式引用的正文不重复追加，其他连接文本仍保留", () => {
        const references: CanvasResourceReference[] = [
            { id: "story", nodeId: "story", kind: "text", label: "文本1", title: "剧情", text: "主角进入仓库。", active: true },
            { id: "style", nodeId: "style", kind: "text", label: "文本2", title: "画风", text: "冷色电影光影。", active: true },
        ];
        for (const token of ["@文本1", "@[node:story]"]) {
            expect(expandStoryboardTextMentions(`${token} 拆成 8 个镜头`, references)).toBe(
                "【项目设定：剧情】\n主角进入仓库。 拆成 8 个镜头\n\n【项目设定：画风】\n冷色电影光影。",
            );
        }
    });

    test("同一文本引用只自动追加一次", () => {
        const reference: CanvasResourceReference = { id: "story", nodeId: "story", kind: "text", label: "文本1", title: "剧情", text: "主角进入仓库。", active: true };
        expect(expandStoryboardTextMentions("6s", [reference, { ...reference, id: "duplicate" }])).toBe("6s\n\n【项目设定：剧情】\n主角进入仓库。");
    });

    test("未连接、空正文和非文本引用不作为剧情追加", () => {
        const reference: CanvasResourceReference = { id: "source", nodeId: "source", kind: "text", label: "文本1", title: "正文", text: "不应追加。", active: true };
        expect(expandStoryboardTextMentions("6s", [
            { ...reference, active: false },
            { ...reference, id: "empty", nodeId: "empty", text: "  " },
            { ...reference, id: "image", nodeId: "image", kind: "image" },
            { ...reference, id: "character", nodeId: "character", kind: "character" },
        ])).toBe("6s");
        expect(expandStoryboardTextMentions("  原始剧情要求\n", [])).toBe("  原始剧情要求\n");
    });
});
