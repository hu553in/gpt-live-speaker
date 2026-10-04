import { defineConfig } from "oxlint";
import core from "ultracite/oxlint/core";
import svelte from "ultracite/oxlint/svelte";

export default defineConfig({
  extends: [core, svelte],
  rules: {
    "func-style": "off",
    "promise/avoid-new": "off",
  },
});
