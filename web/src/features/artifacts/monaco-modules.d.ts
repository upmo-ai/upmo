// Monaco ships no types for its JSON tokenizer.
declare module "monaco-editor/languages/features/json/tokenization" {
  import type { languages } from "monaco-editor/editor/editor.api";

  export function createTokenizationSupport(supportComments: boolean): languages.TokensProvider;
}
