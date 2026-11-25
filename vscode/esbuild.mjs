// Bundles the extension (Node, CommonJS) and the chat webview script (browser).
import { build } from "esbuild";

const common = { bundle: true, sourcemap: true, logLevel: "info" };

await Promise.all([
  build({ ...common, entryPoints: ["src/extension.ts"], outfile: "dist/extension.js", platform: "node", format: "cjs", target: "node20", external: ["vscode"] }),
  build({ ...common, entryPoints: ["src/webview/chat.ts"], outfile: "dist/chat.js", platform: "browser", format: "iife", target: "es2022" }),
]);
