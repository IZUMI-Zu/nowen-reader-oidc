import assert from "node:assert/strict";
import fs from "node:fs";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import react from "@vitejs/plugin-react";
import { createServer } from "vite";

const root = new URL("../", import.meta.url);
const vite = await createServer({
  root: root.pathname,
  configFile: false,
  appType: "custom",
  logLevel: "silent",
  plugins: [react()],
  server: { middlewareMode: true },
});

try {
  const { MetadataCoverPreview } = await vite.ssrLoadModule(
    "/src/components/MetadataCoverPreview.tsx",
  );
  const markup = renderToStaticMarkup(
    React.createElement(MetadataCoverPreview, {
      coverUrl: "https://ul.ehgt.org/fixture-cover.jpg",
      title: "Fixture gallery",
    }),
  );

  assert.match(markup, /<img\b[^>]*src="https:\/\/ul\.ehgt\.org\/fixture-cover\.jpg"/);
  assert.match(markup, /<img\b[^>]*referrerPolicy="no-referrer"/);

  for (const component of ["MetadataSearch.tsx", "GroupMetadataSearch.tsx"]) {
    const source = fs.readFileSync(
      new URL(`../src/components/${component}`, import.meta.url),
      "utf8",
    );
    assert.match(
      source,
      /<MetadataCoverPreview\s+coverUrl=\{result\.coverUrl\}/,
      `${component} must use the protected metadata cover preview`,
    );
  }
} finally {
  await vite.close();
}

console.log("Metadata cover referrer security tests passed.");
