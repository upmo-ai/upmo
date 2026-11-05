declare module "node:fs" {
  export function readFileSync(path: string): Uint8Array;
  export function readFileSync(path: string, encoding: "utf8"): string;
  export function readdirSync(
    path: string,
    options: { withFileTypes: true },
  ): { name: string; isDirectory(): boolean }[];
  export function copyFileSync(from: string, to: string): void;
  export function mkdtempSync(prefix: string): string;
  export function writeFileSync(path: string, data: string): void;
  export function rmSync(path: string, options: { recursive: true; force: true }): void;
}

declare module "node:path" {
  export function dirname(path: string): string;
  export function join(...paths: string[]): string;
}

declare module "node:module" {
  export function createRequire(url: string): { resolve(id: string): string };
}

declare module "node:os" {
  export function tmpdir(): string;
}
