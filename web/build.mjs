import { build } from "esbuild";
await build({
  entryPoints: ["app.jsx"],
  bundle: true,
  minify: true,
  outfile: "../internal/web/dist/app.js",
  legalComments: "eof",
  define: { "process.env.NODE_ENV": '"production"' },
});
