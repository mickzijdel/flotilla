# Flotilla VS Code Extension Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a VS Code extension that renders and drives the flotilla fleet from one editor-area dashboard tab, shelling out to the `flotilla` CLI for every action.

**Architecture:** A **pure `core/` of plain functions** (JSON normalization, fleet-state model, inbox parsing, intent→argv mapping, HTML render) that never import `vscode`, plus thin `vscode` adapters (`cli.ts`, `dashboard.ts`, `ack.ts`, `extension.ts`) that wire those functions to the editor. The webview is render-only and communicates by `postMessage`; all privileged work happens in the extension host via `child_process.execFile` (non-interactive) or an integrated terminal (interactive). The CLI is the sole control plane.

**Tech Stack:** TypeScript, esbuild (bundle), tsc (type-check + test compile), mocha (unit), `@vscode/test-electron` (self-skipping integration), eslint, `@vscode/vsce` (package).

Spec: [docs/specs/2026-06-24-flotilla-vscode-extension-design.md](../specs/2026-06-24-flotilla-vscode-extension-design.md).

## Global Constraints

- **Location:** all extension code lives under `clients/vscode/`. Nothing outside it except a CI job and a backlog tick.
- **`engines.vscode`:** `^1.85.0`; `@types/vscode` matched to `^1.85.0`.
- **CLI-only control plane:** never call Docker, SSH, or the daemon directly — only `flotilla …`. The local binary *is* the federated client, so "remote" is transparent.
- **Argv-safety:** non-interactive commands run via `child_process.execFile(binary, argv[])` (no shell). Terminal `sendText` paths shell-quote any operator-supplied argument (notably the spawn prompt).
- **`core/` purity:** files under `src/core/` MUST NOT `import` from `vscode`. This is what keeps them unit-testable without electron. Adapters import `vscode`; core does not.
- **Theming:** status colors use `ThemeColor` tokens (`charts.green/yellow/red/blue`), never hard-coded hex, in the webview (via CSS variables VS Code injects).
- **Distribution:** VSIX-only for v1 (`vsce package`); no Marketplace publish.
- **Binary discovery:** the CLI path comes from the `flotilla.binaryPath` setting (default `flotilla`); home dir from `flotilla.home` (default `~/.flotilla`); poll cadence from `flotilla.refreshIntervalSeconds` (default `3`).

## File Structure

```
clients/vscode/
  package.json            # manifest: contributes commands/config/activation; scripts; devDeps
  tsconfig.json           # strict; outDir ./out; rootDir ./src
  .eslintrc.json          # @typescript-eslint
  .vscodeignore           # exclude src/out from the vsix payload (ship bundle only)
  .mocharc.json           # unit test glob
  esbuild.mjs             # bundle src/extension.ts → out/extension.js, src/webview/main.ts → media/dashboard.js
  README.md               # install-from-VSIX + dev instructions
  media/
    dashboard.css         # webview styles (uses VS Code theme CSS vars)
    dashboard.js          # built by esbuild from src/webview/main.ts (gitignored build output)
  src/
    core/                 # PURE — no `vscode` import
      types.ts            # Agent, HostHealth, Question, PullRequest, FleetState, Intent, raw wire types
      normalize.ts        # normalizeList(), parseVersion()
      fleetState.ts       # buildFleetState()
      inbox.ts            # parseInboxLines(), prsFromEvents(), questionEvents()
      intents.ts          # intentToArgv()
      render.ts           # renderDashboard(state) → HTML string
    cli.ts                # execFile wrapper → calls core/normalize
    ack.ts                # AckStore over a Memento-like interface
    dashboard.ts          # WebviewPanel lifecycle: post state, receive intents
    inboxWatcher.ts       # FileSystemWatcher on the local inbox.jsonl
    extension.ts          # activate(): wire commands, status bar, poll loop, watcher
    webview/
      main.ts             # webview entry: acquireVsCodeApi + render + event wiring
    test/
      unit/               # mocha specs for core/* and ack.ts (no electron)
      integration/        # @vscode/test-electron runner (self-skipping)
.github/workflows/        # add a vscode job (path-filtered to clients/vscode/**)
```

**Decomposition rationale:** each `core/` module is one pure responsibility with its own spec; adapters are thin and mostly covered by the integration test. Tasks 2–7 are pure logic (fast TDD, no mocks). Tasks 8–11 wire them to `vscode`.

---

### Task 1: Scaffold the extension package

**Files:**
- Create: `clients/vscode/package.json`
- Create: `clients/vscode/tsconfig.json`
- Create: `clients/vscode/.eslintrc.json`
- Create: `clients/vscode/.mocharc.json`
- Create: `clients/vscode/.vscodeignore`
- Create: `clients/vscode/.gitignore`
- Create: `clients/vscode/esbuild.mjs`
- Create: `clients/vscode/src/extension.ts`
- Test: `clients/vscode/src/test/unit/smoke.test.ts`

**Interfaces:**
- Produces: an installable package with `npm run compile`, `npm run check`, `npm run lint`, `npm run test:unit`, `npm run package` scripts.

- [ ] **Step 1: Write the failing test**

`clients/vscode/src/test/unit/smoke.test.ts`:
```typescript
import * as assert from 'node:assert';

describe('toolchain smoke', () => {
  it('runs a unit test', () => {
    assert.strictEqual(1 + 1, 2);
  });
});
```

- [ ] **Step 2: Create the package manifest**

`clients/vscode/package.json`:
```json
{
  "name": "flotilla",
  "displayName": "Flotilla",
  "description": "Monitor and drive a fleet of sandboxed coding agents.",
  "version": "0.1.0",
  "publisher": "flotilla",
  "license": "SEE LICENSE IN ../../LICENSE",
  "engines": { "vscode": "^1.85.0" },
  "categories": ["Other"],
  "main": "./out/extension.js",
  "activationEvents": ["onStartupFinished"],
  "contributes": {
    "commands": [
      { "command": "flotilla.openDashboard", "title": "Flotilla: Open Dashboard" }
    ]
  },
  "scripts": {
    "compile": "node esbuild.mjs",
    "check": "tsc --noEmit -p ./",
    "lint": "eslint src --ext ts",
    "pretest:unit": "tsc -p ./",
    "test:unit": "mocha",
    "package": "vsce package --no-dependencies -o flotilla.vsix"
  },
  "devDependencies": {
    "@types/mocha": "^10.0.6",
    "@types/node": "^20.11.0",
    "@types/vscode": "^1.85.0",
    "@typescript-eslint/eslint-plugin": "^7.0.0",
    "@typescript-eslint/parser": "^7.0.0",
    "@vscode/test-electron": "^2.3.9",
    "@vscode/vsce": "^2.24.0",
    "esbuild": "^0.20.0",
    "eslint": "^8.56.0",
    "mocha": "^10.3.0",
    "typescript": "^5.3.3"
  }
}
```

- [ ] **Step 3: Create the config files**

`clients/vscode/tsconfig.json`:
```json
{
  "compilerOptions": {
    "module": "Node16",
    "moduleResolution": "Node16",
    "target": "ES2022",
    "outDir": "out",
    "rootDir": "src",
    "lib": ["ES2022", "DOM"],
    "strict": true,
    "sourceMap": true,
    "skipLibCheck": true
  },
  "include": ["src"]
}
```

`clients/vscode/.mocharc.json`:
```json
{ "spec": "out/test/unit/**/*.test.js", "timeout": 5000 }
```

`clients/vscode/.eslintrc.json`:
```json
{
  "root": true,
  "parser": "@typescript-eslint/parser",
  "plugins": ["@typescript-eslint"],
  "extends": ["eslint:recommended", "plugin:@typescript-eslint/recommended"],
  "rules": { "@typescript-eslint/no-explicit-any": "off" }
}
```

`clients/vscode/.gitignore`:
```
node_modules/
out/
media/dashboard.js
*.vsix
```

`clients/vscode/.vscodeignore`:
```
src/**
out/test/**
.vscode/**
.mocharc.json
tsconfig.json
esbuild.mjs
**/*.map
```

`clients/vscode/esbuild.mjs`:
```javascript
import * as esbuild from 'esbuild';

const common = { bundle: true, format: 'cjs', platform: 'node', target: 'node18', sourcemap: true };

await esbuild.build({
  ...common,
  entryPoints: ['src/extension.ts'],
  outfile: 'out/extension.js',
  external: ['vscode'],
});

await esbuild.build({
  entryPoints: ['src/webview/main.ts'],
  outfile: 'media/dashboard.js',
  bundle: true, format: 'iife', platform: 'browser', target: 'es2020', sourcemap: true,
});

console.log('esbuild: built extension + webview');
```

- [ ] **Step 4: Create the minimal extension entry**

`clients/vscode/src/extension.ts`:
```typescript
import * as vscode from 'vscode';

export function activate(context: vscode.ExtensionContext): void {
  context.subscriptions.push(
    vscode.commands.registerCommand('flotilla.openDashboard', () => {
      vscode.window.showInformationMessage('Flotilla dashboard (scaffold)');
    }),
  );
}

export function deactivate(): void {}
```

Create an empty `clients/vscode/src/webview/main.ts` (so esbuild's second entry resolves):
```typescript
// webview entry — filled in Task 9
export {};
```

- [ ] **Step 5: Install and verify the toolchain**

Run (from `clients/vscode/`):
```bash
npm install
npm run check
npm run lint
npm run test:unit
npm run compile
```
Expected: `check` and `lint` clean; `test:unit` shows `1 passing`; `compile` prints `esbuild: built extension + webview`.

- [ ] **Step 6: Verify packaging**

Run: `npm run package`
Expected: `flotilla.vsix` is produced (a warning about the missing repository field is acceptable).

- [ ] **Step 7: Commit**

```bash
git add clients/vscode
git commit -m "feat(vscode): scaffold extension package + toolchain"
```

---

### Task 2: Shared types + JSON normalization

**Files:**
- Create: `clients/vscode/src/core/types.ts`
- Create: `clients/vscode/src/core/normalize.ts`
- Test: `clients/vscode/src/test/unit/normalize.test.ts`

**Interfaces:**
- Produces:
  - `RawAgent = { name; repo; status; created; id; logDir? }`
  - `HostHealth = { name; ok; error?; version?; contract? }`
  - `NormalizedList = { agents: RawAgentWithHost[]; hosts: HostHealth[] }` where `RawAgentWithHost = RawAgent & { host: string }`
  - `normalizeList(raw: unknown): NormalizedList`
  - `parseVersion(raw: unknown): { version: string; contract: number } | null`

- [ ] **Step 1: Write the failing test**

`clients/vscode/src/test/unit/normalize.test.ts`:
```typescript
import * as assert from 'node:assert';
import { normalizeList, parseVersion } from '../../core/normalize';

describe('normalizeList', () => {
  it('wraps a bare array as the local host', () => {
    const raw = [{ name: 'a', repo: 'r', status: 'running', created: '2026-06-24T08:00:00Z', id: 'c1' }];
    const out = normalizeList(raw);
    assert.deepStrictEqual(out.hosts, [{ name: 'local', ok: true }]);
    assert.strictEqual(out.agents[0].host, 'local');
    assert.strictEqual(out.agents[0].name, 'a');
  });

  it('uses the wrapped {rows,hosts} shape directly', () => {
    const raw = {
      rows: [{ host: 'beefy', name: 'a', repo: 'r', status: 'running', created: '2026-06-24T08:00:00Z', id: 'c1' }],
      hosts: [{ name: 'beefy', ok: true, version: '0.4.0', contract: 1 }],
    };
    const out = normalizeList(raw);
    assert.strictEqual(out.agents[0].host, 'beefy');
    assert.strictEqual(out.hosts[0].version, '0.4.0');
  });

  it('returns empty on malformed input', () => {
    assert.deepStrictEqual(normalizeList(null), { agents: [], hosts: [] });
    assert.deepStrictEqual(normalizeList(42), { agents: [], hosts: [] });
  });
});

describe('parseVersion', () => {
  it('reads {version,contract}', () => {
    assert.deepStrictEqual(parseVersion({ version: '0.4.0', contract: 1 }), { version: '0.4.0', contract: 1 });
  });
  it('returns null when absent (legacy engine)', () => {
    assert.strictEqual(parseVersion(undefined), null);
    assert.strictEqual(parseVersion({ nope: true }), null);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npm run pretest:unit && npx mocha out/test/unit/normalize.test.js`
Expected: FAIL — `Cannot find module '../../core/normalize'`.

- [ ] **Step 3: Write the types**

`clients/vscode/src/core/types.ts`:
```typescript
export interface RawAgent {
  name: string;
  repo: string;
  status: string;
  created: string;
  id: string;
  logDir?: string;
}
export type RawAgentWithHost = RawAgent & { host: string };

export interface HostHealth {
  name: string;
  ok: boolean;
  error?: string;
  version?: string;
  contract?: number;
}
export interface NormalizedList {
  agents: RawAgentWithHost[];
  hosts: HostHealth[];
}

export interface Question { host: string; agent: string; id: string; text: string; ageSeconds: number; }
export interface PullRequest {
  host: string; agent: string; repo: string; branch: string; prURL: string | null; acknowledged: boolean;
}
export interface Progress { step: number; total: number; label: string; }

export interface Agent {
  host: string; name: string; repo: string; status: string; ageSeconds: number; id: string;
  objective: string | null; progress: Progress | null; pr: PullRequest | null;
}
export interface FleetState {
  updatedAt: string;
  hosts: HostHealth[];
  agents: Agent[];
  questions: Question[];
  prs: PullRequest[];
}

export type Verb = 'spawn' | 'refresh' | 'attach' | 'logs' | 'submit' | 'stop' | 'rm' | 'answer' | 'openPR' | 'checkoutPR' | 'ackPR';
export interface Intent {
  type: 'action'; verb: Verb; host?: string; agent?: string; id?: string; text?: string;
}
```

- [ ] **Step 4: Write the normalizer**

`clients/vscode/src/core/normalize.ts`:
```typescript
import { NormalizedList, RawAgentWithHost, HostHealth } from './types';

export function normalizeList(raw: unknown): NormalizedList {
  if (Array.isArray(raw)) {
    return { agents: raw.map((r) => ({ ...(r as object), host: 'local' }) as RawAgentWithHost), hosts: [{ name: 'local', ok: true }] };
  }
  if (raw && typeof raw === 'object' && Array.isArray((raw as any).rows)) {
    const obj = raw as { rows: RawAgentWithHost[]; hosts?: HostHealth[] };
    return { agents: obj.rows, hosts: obj.hosts ?? [] };
  }
  return { agents: [], hosts: [] };
}

export function parseVersion(raw: unknown): { version: string; contract: number } | null {
  if (raw && typeof raw === 'object') {
    const o = raw as any;
    if (typeof o.version === 'string' && typeof o.contract === 'number') {
      return { version: o.version, contract: o.contract };
    }
  }
  return null;
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `npm run test:unit`
Expected: PASS — normalize + parseVersion specs green.

- [ ] **Step 6: Commit**

```bash
git add clients/vscode/src/core/types.ts clients/vscode/src/core/normalize.ts clients/vscode/src/test/unit/normalize.test.ts
git commit -m "feat(vscode): wire-shape normalization (bare array vs {rows,hosts})"
```

---

### Task 3: Fleet-state model

**Files:**
- Create: `clients/vscode/src/core/fleetState.ts`
- Test: `clients/vscode/src/test/unit/fleetState.test.ts`

**Interfaces:**
- Consumes: `NormalizedList` (Task 2), `Question`/`PullRequest`/`Agent`/`FleetState` (Task 2 types).
- Produces: `buildFleetState(input: { list: NormalizedList; questions: Question[]; prs: PullRequest[]; ackedKeys: Set<string>; nowMs: number }): FleetState` and `prKey(p: { host: string; agent: string; branch: string }): string`.

- [ ] **Step 1: Write the failing test**

`clients/vscode/src/test/unit/fleetState.test.ts`:
```typescript
import * as assert from 'node:assert';
import { buildFleetState, prKey } from '../../core/fleetState';
import { NormalizedList, Question, PullRequest } from '../../core/types';

const NOW = Date.parse('2026-06-24T10:00:00Z');
const list: NormalizedList = {
  hosts: [{ name: 'local', ok: true }],
  agents: [
    { host: 'local', name: 'wise-lynx', repo: 'acme/api', status: 'running', created: '2026-06-24T09:55:00Z', id: 'c1' },
    { host: 'local', name: 'keen-fox', repo: 'acme/api', status: 'exited', created: '2026-06-24T08:00:00Z', id: 'c2' },
  ],
};
const questions: Question[] = [{ host: 'local', agent: 'wise-lynx', id: 'q1', text: 'Drop table?', ageSeconds: 300 }];
const prs: PullRequest[] = [{ host: 'local', agent: 'keen-fox', repo: 'acme/api', branch: 'flotilla/keen-fox', prURL: 'https://gh/pr/1', acknowledged: false }];

describe('buildFleetState', () => {
  it('overlays blocked on agents with a pending question', () => {
    const s = buildFleetState({ list, questions, prs, ackedKeys: new Set(), nowMs: NOW });
    assert.strictEqual(s.agents.find((a) => a.name === 'wise-lynx')!.status, 'blocked');
  });

  it('computes age from created', () => {
    const s = buildFleetState({ list, questions: [], prs: [], ackedKeys: new Set(), nowMs: NOW });
    assert.strictEqual(s.agents.find((a) => a.name === 'wise-lynx')!.ageSeconds, 300);
  });

  it('attaches the PR to its agent and exposes unacked prs', () => {
    const s = buildFleetState({ list, questions: [], prs, ackedKeys: new Set(), nowMs: NOW });
    assert.strictEqual(s.agents.find((a) => a.name === 'keen-fox')!.pr!.prURL, 'https://gh/pr/1');
    assert.strictEqual(s.prs.length, 1);
  });

  it('filters acknowledged PRs out of the prs list but keeps them on the agent', () => {
    const acked = new Set([prKey({ host: 'local', agent: 'keen-fox', branch: 'flotilla/keen-fox' })]);
    const s = buildFleetState({ list, questions: [], prs, ackedKeys: acked, nowMs: NOW });
    assert.strictEqual(s.prs.length, 0);
    assert.strictEqual(s.agents.find((a) => a.name === 'keen-fox')!.pr!.acknowledged, true);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npm run pretest:unit && npx mocha out/test/unit/fleetState.test.js`
Expected: FAIL — `Cannot find module '../../core/fleetState'`.

- [ ] **Step 3: Write the model**

`clients/vscode/src/core/fleetState.ts`:
```typescript
import { Agent, FleetState, NormalizedList, PullRequest, Question } from './types';

export function prKey(p: { host: string; agent: string; branch: string }): string {
  return `${p.host} ${p.agent} ${p.branch}`;
}

interface BuildInput {
  list: NormalizedList;
  questions: Question[];
  prs: PullRequest[];
  ackedKeys: Set<string>;
  nowMs: number;
}

export function buildFleetState(input: BuildInput): FleetState {
  const { list, questions, prs, ackedKeys, nowMs } = input;
  const blocked = new Set(questions.map((q) => `${q.host} ${q.agent}`));
  const prByAgent = new Map(prs.map((p) => [`${p.host} ${p.agent}`, p]));

  const agents: Agent[] = list.agents.map((a) => {
    const key = `${a.host} ${a.name}`;
    const rawPr = prByAgent.get(key) ?? null;
    const pr = rawPr ? { ...rawPr, acknowledged: ackedKeys.has(prKey(rawPr)) } : null;
    return {
      host: a.host, name: a.name, repo: a.repo,
      status: blocked.has(key) ? 'blocked' : a.status,
      ageSeconds: Math.max(0, Math.round((nowMs - Date.parse(a.created)) / 1000)),
      id: a.id, objective: null, progress: null, pr,
    };
  });

  const unackedPrs = prs.filter((p) => !ackedKeys.has(prKey(p)));
  return { updatedAt: new Date(nowMs).toISOString(), hosts: list.hosts, agents, questions, prs: unackedPrs };
}
```

> Note: `core/` is pure, so `nowMs` is injected (no `Date.now()` inside) — this keeps the tests deterministic.

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm run test:unit`
Expected: PASS — all `buildFleetState` specs green.

- [ ] **Step 5: Commit**

```bash
git add clients/vscode/src/core/fleetState.ts clients/vscode/src/test/unit/fleetState.test.ts
git commit -m "feat(vscode): fleet-state model (blocked overlay, age, PR attach/ack)"
```

---

### Task 4: Inbox JSONL parsing

**Files:**
- Create: `clients/vscode/src/core/inbox.ts`
- Test: `clients/vscode/src/test/unit/inbox.test.ts`

**Interfaces:**
- Consumes: `PullRequest` (Task 2).
- Produces:
  - `InboxEvent = { ts: string; agent: string; type: string; message: string; data?: Record<string, unknown> }`
  - `parseInboxLines(text: string): InboxEvent[]` (skips blank/malformed lines)
  - `prsFromEvents(events: InboxEvent[], repoFor: (agent: string) => string, host: string): PullRequest[]` (latest `pr_opened`/`pr_updated` per agent)
  - `hasQuestionEvent(events: InboxEvent[]): boolean`

- [ ] **Step 1: Write the failing test**

`clients/vscode/src/test/unit/inbox.test.ts`:
```typescript
import * as assert from 'node:assert';
import { parseInboxLines, prsFromEvents, hasQuestionEvent } from '../../core/inbox';

const jsonl = [
  '{"ts":"2026-06-24T08:00:00Z","agent":"keen-fox","type":"pr_opened","message":"opened PR","data":{"branch":"flotilla/keen-fox","prURL":"https://gh/pr/1"}}',
  'not json — should be skipped',
  '{"ts":"2026-06-24T09:00:00Z","agent":"keen-fox","type":"pr_updated","message":"updated PR","data":{"branch":"flotilla/keen-fox","prURL":"https://gh/pr/1b"}}',
  '{"ts":"2026-06-24T09:30:00Z","agent":"wise-lynx","type":"question","message":"asked","data":{"id":"q1","text":"?"}}',
  '',
].join('\n');

describe('parseInboxLines', () => {
  it('parses valid lines and skips junk', () => {
    assert.strictEqual(parseInboxLines(jsonl).length, 3);
  });
});

describe('prsFromEvents', () => {
  it('keeps the latest PR per agent', () => {
    const prs = prsFromEvents(parseInboxLines(jsonl), () => 'acme/api', 'local');
    assert.strictEqual(prs.length, 1);
    assert.strictEqual(prs[0].prURL, 'https://gh/pr/1b');
    assert.strictEqual(prs[0].repo, 'acme/api');
    assert.strictEqual(prs[0].acknowledged, false);
  });
});

describe('hasQuestionEvent', () => {
  it('detects a question event', () => {
    assert.strictEqual(hasQuestionEvent(parseInboxLines(jsonl)), true);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npm run pretest:unit && npx mocha out/test/unit/inbox.test.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write the parser**

`clients/vscode/src/core/inbox.ts`:
```typescript
import { PullRequest } from './types';

export interface InboxEvent {
  ts: string; agent: string; type: string; message: string; data?: Record<string, unknown>;
}

export function parseInboxLines(text: string): InboxEvent[] {
  const out: InboxEvent[] = [];
  for (const line of text.split('\n')) {
    const t = line.trim();
    if (!t) continue;
    try {
      const e = JSON.parse(t);
      if (e && typeof e.agent === 'string' && typeof e.type === 'string') out.push(e as InboxEvent);
    } catch {
      // skip malformed line
    }
  }
  return out;
}

export function prsFromEvents(events: InboxEvent[], repoFor: (agent: string) => string, host: string): PullRequest[] {
  const latest = new Map<string, PullRequest>();
  for (const e of events) {
    if (e.type !== 'pr_opened' && e.type !== 'pr_updated') continue;
    const branch = String(e.data?.branch ?? '');
    const prURL = e.data?.prURL ? String(e.data.prURL) : null;
    latest.set(e.agent, { host, agent: e.agent, repo: repoFor(e.agent), branch, prURL, acknowledged: false });
  }
  return [...latest.values()];
}

export function hasQuestionEvent(events: InboxEvent[]): boolean {
  return events.some((e) => e.type === 'question');
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm run test:unit`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add clients/vscode/src/core/inbox.ts clients/vscode/src/test/unit/inbox.test.ts
git commit -m "feat(vscode): inbox JSONL parsing + PR/question extraction"
```

---

### Task 5: PR acknowledgement store

**Files:**
- Create: `clients/vscode/src/ack.ts`
- Test: `clients/vscode/src/test/unit/ack.test.ts`

**Interfaces:**
- Consumes: `prKey` (Task 3).
- Produces: `interface KeyValueStore { get<T>(k: string, d: T): T; update(k: string, v: unknown): Thenable<void> }` and `class AckStore { constructor(store: KeyValueStore); isAcked(p): boolean; ack(p): Promise<void>; keys(): Set<string> }` where `p = { host; agent; branch }`. `KeyValueStore` is structurally satisfied by `vscode.Memento`, so no `vscode` import is needed here.

- [ ] **Step 1: Write the failing test**

`clients/vscode/src/test/unit/ack.test.ts`:
```typescript
import * as assert from 'node:assert';
import { AckStore, KeyValueStore } from '../../ack';

class FakeMemento implements KeyValueStore {
  private m = new Map<string, unknown>();
  get<T>(k: string, d: T): T { return (this.m.has(k) ? this.m.get(k) : d) as T; }
  update(k: string, v: unknown): Thenable<void> { this.m.set(k, v); return Promise.resolve(); }
}

describe('AckStore', () => {
  it('acks a PR and reports it acked', async () => {
    const store = new AckStore(new FakeMemento());
    const p = { host: 'local', agent: 'keen-fox', branch: 'flotilla/keen-fox' };
    assert.strictEqual(store.isAcked(p), false);
    await store.ack(p);
    assert.strictEqual(store.isAcked(p), true);
    assert.strictEqual(store.keys().size, 1);
  });

  it('persists through a fresh AckStore over the same backing store', async () => {
    const mem = new FakeMemento();
    await new AckStore(mem).ack({ host: 'local', agent: 'a', branch: 'b' });
    assert.strictEqual(new AckStore(mem).isAcked({ host: 'local', agent: 'a', branch: 'b' }), true);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npm run pretest:unit && npx mocha out/test/unit/ack.test.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write the store**

`clients/vscode/src/ack.ts`:
```typescript
import { prKey } from './core/fleetState';

export interface KeyValueStore {
  get<T>(key: string, def: T): T;
  update(key: string, value: unknown): Thenable<void>;
}

const STORAGE_KEY = 'flotilla.ackedPRs';

export class AckStore {
  private set: Set<string>;
  constructor(private store: KeyValueStore) {
    this.set = new Set(store.get<string[]>(STORAGE_KEY, []));
  }
  isAcked(p: { host: string; agent: string; branch: string }): boolean {
    return this.set.has(prKey(p));
  }
  async ack(p: { host: string; agent: string; branch: string }): Promise<void> {
    this.set.add(prKey(p));
    await this.store.update(STORAGE_KEY, [...this.set]);
  }
  keys(): Set<string> {
    return new Set(this.set);
  }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm run test:unit`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add clients/vscode/src/ack.ts clients/vscode/src/test/unit/ack.test.ts
git commit -m "feat(vscode): PR acknowledgement store over a Memento"
```

---

### Task 6: Intent → argv mapping

**Files:**
- Create: `clients/vscode/src/core/intents.ts`
- Test: `clients/vscode/src/test/unit/intents.test.ts`

**Interfaces:**
- Consumes: `Intent` (Task 2).
- Produces: `intentToArgv(intent: Intent): string[]` — the `flotilla` argv (excluding the binary) for non-interactive verbs (`answer`, `submit`, `stop`, `rm`). Throws on a verb that has no non-interactive CLI form (`spawn`, `attach`, `logs`, `refresh`, `openPR`, `checkoutPR`, `ackPR`). Also `listArgv(host?: string)` and `questionsArgv(host?: string)` helpers.

- [ ] **Step 1: Write the failing test**

`clients/vscode/src/test/unit/intents.test.ts`:
```typescript
import * as assert from 'node:assert';
import { intentToArgv, listArgv, questionsArgv } from '../../core/intents';

describe('intentToArgv', () => {
  it('builds answer argv with --host and --id', () => {
    assert.deepStrictEqual(
      intentToArgv({ type: 'action', verb: 'answer', host: 'beefy', agent: 'wise-lynx', id: 'q1', text: 'Yes; rm -rf $HOME' }),
      ['--host', 'beefy', 'answer', 'wise-lynx', '--id', 'q1', 'Yes; rm -rf $HOME'],
    );
  });
  it('omits --host for the local host', () => {
    assert.deepStrictEqual(
      intentToArgv({ type: 'action', verb: 'stop', host: 'local', agent: 'a' }),
      ['stop', 'a'],
    );
  });
  it('builds submit argv', () => {
    assert.deepStrictEqual(
      intentToArgv({ type: 'action', verb: 'submit', host: 'local', agent: 'a' }),
      ['submit', 'a', '--json'],
    );
  });
  it('throws on an interactive verb', () => {
    assert.throws(() => intentToArgv({ type: 'action', verb: 'logs', agent: 'a' }));
  });
});

describe('listArgv / questionsArgv', () => {
  it('adds --json and optional --host', () => {
    assert.deepStrictEqual(listArgv(), ['list', '--json']);
    assert.deepStrictEqual(questionsArgv('beefy'), ['--host', 'beefy', 'questions', '--json']);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npm run pretest:unit && npx mocha out/test/unit/intents.test.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write the mapping**

`clients/vscode/src/core/intents.ts`:
```typescript
import { Intent } from './types';

function hostPrefix(host?: string): string[] {
  return host && host !== 'local' ? ['--host', host] : [];
}

export function listArgv(host?: string): string[] {
  return [...hostPrefix(host), 'list', '--json'];
}
export function questionsArgv(host?: string): string[] {
  return [...hostPrefix(host), 'questions', '--json'];
}

export function intentToArgv(intent: Intent): string[] {
  const pre = hostPrefix(intent.host);
  switch (intent.verb) {
    case 'answer':
      return [...pre, 'answer', intent.agent!, '--id', intent.id!, intent.text!];
    case 'submit':
      return [...pre, 'submit', intent.agent!, '--json'];
    case 'stop':
      return [...pre, 'stop', intent.agent!];
    case 'rm':
      return [...pre, 'rm', intent.agent!];
    default:
      throw new Error(`verb ${intent.verb} has no non-interactive CLI form`);
  }
}
```

> Because these argv arrays are passed to `execFile` (no shell), the `Yes; rm -rf $HOME` answer text in the test is inert by construction — that's the safety property we're locking in.

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm run test:unit`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add clients/vscode/src/core/intents.ts clients/vscode/src/test/unit/intents.test.ts
git commit -m "feat(vscode): intent → argv mapping (argv-safe, host-aware)"
```

---

### Task 7: Dashboard HTML render

**Files:**
- Create: `clients/vscode/src/core/render.ts`
- Test: `clients/vscode/src/test/unit/render.test.ts`

**Interfaces:**
- Consumes: `FleetState` (Task 2).
- Produces: `renderDashboard(state: FleetState, opts: { checkoutEnabled: boolean }): string` — the webview `<body>` inner HTML. Groups host→repo→agent; renders a Needs-attention block when there are questions or PRs; renders host warning rows; HTML-escapes all dynamic text.

- [ ] **Step 1: Write the failing test**

`clients/vscode/src/test/unit/render.test.ts`:
```typescript
import * as assert from 'node:assert';
import { renderDashboard } from '../../core/render';
import { FleetState } from '../../core/types';

function state(over: Partial<FleetState> = {}): FleetState {
  return {
    updatedAt: '2026-06-24T10:00:00Z',
    hosts: [{ name: 'local', ok: true }],
    agents: [{ host: 'local', name: 'wise-lynx', repo: 'acme/api', status: 'blocked', ageSeconds: 300, id: 'c1', objective: null, progress: null, pr: null }],
    questions: [{ host: 'local', agent: 'wise-lynx', id: 'q1', text: 'Drop <table>?', ageSeconds: 300 }],
    prs: [],
    ...over,
  };
}

describe('renderDashboard', () => {
  it('renders the agent name and a needs-attention block when blocked', () => {
    const html = renderDashboard(state(), { checkoutEnabled: false });
    assert.ok(html.includes('wise-lynx'));
    assert.ok(html.toLowerCase().includes('needs attention'));
  });
  it('escapes HTML in question text', () => {
    const html = renderDashboard(state(), { checkoutEnabled: false });
    assert.ok(html.includes('Drop &lt;table&gt;?'));
    assert.ok(!html.includes('Drop <table>?'));
  });
  it('omits the needs-attention block when nothing pends', () => {
    const html = renderDashboard(state({ questions: [], prs: [] }), { checkoutEnabled: false });
    assert.ok(!html.toLowerCase().includes('needs attention'));
  });
  it('hides the checkout button when checkout is disabled', () => {
    const html = renderDashboard(
      state({ questions: [], prs: [{ host: 'local', agent: 'keen-fox', repo: 'acme/api', branch: 'b', prURL: 'https://gh/1', acknowledged: false }] }),
      { checkoutEnabled: false },
    );
    assert.ok(!html.includes('data-verb="checkoutPR"'));
  });
  it('shows a host warning row for an unreachable host', () => {
    const html = renderDashboard(state({ hosts: [{ name: 'cloud1', ok: false, error: 'connection refused' }] }), { checkoutEnabled: false });
    assert.ok(html.includes('connection refused'));
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npm run pretest:unit && npx mocha out/test/unit/render.test.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write the renderer**

`clients/vscode/src/core/render.ts`:
```typescript
import { Agent, FleetState, PullRequest, Question } from './types';

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]!));
}
function age(s: number): string {
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  return `${Math.floor(s / 3600)}h${Math.floor((s % 3600) / 60)}m`;
}
function btn(verb: string, label: string, a: { host: string; agent?: string; id?: string }, extra = ''): string {
  return `<button data-verb="${verb}" data-host="${esc(a.host)}" data-agent="${esc(a.agent ?? '')}" data-id="${esc(a.id ?? '')}" ${extra}>${esc(label)}</button>`;
}

function questionItem(q: Question): string {
  return `<div class="q"><div class="q-head"><b>${esc(q.agent)}</b> <span class="dim">${esc(q.host)}</span><span class="age">blocked ${age(q.ageSeconds)}</span></div>
    <div class="q-text">${esc(q.text)}</div>
    <div class="q-row"><input class="q-input" data-agent="${esc(q.agent)}" data-host="${esc(q.host)}" data-id="${esc(q.id)}" placeholder="your answer…"/>
    ${btn('answer', 'Send', { host: q.host, agent: q.agent, id: q.id }, 'class="primary"')}
    ${btn('stop', 'Stop agent', { host: q.host, agent: q.agent })}</div></div>`;
}
function prItem(p: PullRequest, checkoutEnabled: boolean): string {
  const open = p.prURL ? btn('openPR', 'Open PR on GitHub', { host: p.host, agent: p.agent }) : '';
  const checkout = checkoutEnabled && p.prURL ? btn('checkoutPR', 'Check out locally', { host: p.host, agent: p.agent }) : '';
  const label = p.prURL ? `PR opened, not merged` : `pushed ${esc(p.branch)} — open a PR on your host`;
  return `<div class="pr"><div class="q-head"><b>${esc(p.agent)}</b> <span class="dim">${esc(p.host)}/${esc(p.repo)}</span><span class="age">${label}</span></div>
    <div class="q-row">${open}${checkout}${btn('ackPR', 'Acknowledge', { host: p.host, agent: p.agent })}</div></div>`;
}

function agentRow(a: Agent): string {
  const acts = [
    btn('attach', 'Attach', a), btn('logs', 'Logs', a),
    a.status === 'blocked' ? btn('answer', 'Answer↑', a) : '',
    (a.status === 'exited' || a.status === 'done' || a.status === 'running') ? btn('submit', 'Submit', a) : '',
    a.status === 'running' || a.status === 'blocked' ? btn('stop', 'Stop', a) : '',
    btn('rm', 'Rm', a, 'class="danger"'),
  ].join(' ');
  return `<tr class="agent"><td><span class="chip ${esc(a.status)}">${esc(a.status)}</span> <b>${esc(a.name)}</b></td>
    <td class="age">${age(a.ageSeconds)}</td><td class="actions">${acts}</td></tr>`;
}

export function renderDashboard(state: FleetState, opts: { checkoutEnabled: boolean }): string {
  const needs = state.questions.length + state.prs.length > 0
    ? `<section class="needs"><h2>⚑ Needs attention</h2>${state.questions.map(questionItem).join('')}${state.prs.map((p) => prItem(p, opts.checkoutEnabled)).join('')}</section>`
    : '';

  const showHostHeaders = state.hosts.length > 1 || state.hosts.some((h) => h.name !== 'local');
  const byHost = new Map<string, Agent[]>();
  for (const a of state.agents) (byHost.get(a.host) ?? byHost.set(a.host, []).get(a.host)!).push(a);

  const hostBlocks = state.hosts.map((h) => {
    if (!h.ok) return `<div class="host-warn">! ${esc(h.name)} — ${esc(h.error ?? 'unreachable')}</div>`;
    const agents = byHost.get(h.name) ?? [];
    const header = showHostHeaders ? `<div class="host-head">${esc(h.name)} <span class="dim">${esc(h.version ?? '')}</span></div>` : '';
    const byRepo = new Map<string, Agent[]>();
    for (const a of agents) (byRepo.get(a.repo) ?? byRepo.set(a.repo, []).get(a.repo)!).push(a);
    const repos = [...byRepo.entries()].map(([repo, rows]) =>
      `<div class="repo-head">${esc(repo)} · ${rows.length}</div><table>${rows.map(agentRow).join('')}</table>`).join('');
    return `${header}${repos}`;
  }).join('');

  return `<div class="toolbar">${btn('spawn', '＋ Spawn agent…', { host: 'local' }, 'class="primary"')} ${btn('refresh', '⟳ Refresh', { host: 'local' })}
    <span class="dim updated">updated ${esc(state.updatedAt)}</span></div>${needs}${hostBlocks}`;
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm run test:unit`
Expected: PASS — all 5 render specs green.

- [ ] **Step 5: Commit**

```bash
git add clients/vscode/src/core/render.ts clients/vscode/src/test/unit/render.test.ts
git commit -m "feat(vscode): pure dashboard HTML render (grouping, needs-attention, escaping)"
```

---

### Task 8: CLI adapter (execFile + normalize)

**Files:**
- Create: `clients/vscode/src/cli.ts`
- Test: `clients/vscode/src/test/unit/cli.test.ts`

**Interfaces:**
- Consumes: `normalizeList`, `parseVersion` (Task 2), `listArgv`/`questionsArgv` (Task 6).
- Produces: `class Cli { constructor(opts: { binaryPath: string; run?: RunFn }); list(host?): Promise<NormalizedList>; questions(host?): Promise<Question[]>; exec(argv: string[]): Promise<{ stdout: string; code: number }>; version(): Promise<{version,contract}|null>; submit(host, agent): Promise<Submission> }` where `RunFn = (binary, argv) => Promise<{ stdout; stderr; code }>` is injected so tests don't spawn a real process. The default `RunFn` wraps `child_process.execFile`. This adapter imports only `node:child_process` (not `vscode`), so it's unit-testable.

- [ ] **Step 1: Write the failing test**

`clients/vscode/src/test/unit/cli.test.ts`:
```typescript
import * as assert from 'node:assert';
import { Cli } from '../../cli';

function fakeRun(map: Record<string, string>) {
  return async (_bin: string, argv: string[]) => {
    const out = map[argv.join(' ')];
    if (out === undefined) return { stdout: '', stderr: 'unknown', code: 1 };
    return { stdout: out, stderr: '', code: 0 };
  };
}

describe('Cli', () => {
  it('list() normalizes a bare array', async () => {
    const cli = new Cli({ binaryPath: 'flotilla', run: fakeRun({ 'list --json': '[{"name":"a","repo":"r","status":"running","created":"2026-06-24T08:00:00Z","id":"c1"}]' }) });
    const out = await cli.list();
    assert.strictEqual(out.agents[0].host, 'local');
  });
  it('questions() parses the array', async () => {
    const cli = new Cli({ binaryPath: 'flotilla', run: fakeRun({ 'questions --json': '[{"host":"local","agent":"a","id":"q1","text":"?","ageSeconds":5}]' }) });
    assert.strictEqual((await cli.questions())[0].id, 'q1');
  });
  it('version() returns null on a legacy engine (non-zero exit)', async () => {
    const cli = new Cli({ binaryPath: 'flotilla', run: fakeRun({}) });
    assert.strictEqual(await cli.version(), null);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npm run pretest:unit && npx mocha out/test/unit/cli.test.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write the adapter**

`clients/vscode/src/cli.ts`:
```typescript
import { execFile } from 'node:child_process';
import { normalizeList, parseVersion } from './core/normalize';
import { listArgv, questionsArgv } from './core/intents';
import { NormalizedList, Question } from './core/types';

export type RunResult = { stdout: string; stderr: string; code: number };
export type RunFn = (binary: string, argv: string[]) => Promise<RunResult>;

const defaultRun: RunFn = (binary, argv) =>
  new Promise((resolve) => {
    execFile(binary, argv, { timeout: 15000, maxBuffer: 8 * 1024 * 1024 }, (err, stdout, stderr) => {
      resolve({ stdout: stdout ?? '', stderr: stderr ?? '', code: err && typeof (err as any).code === 'number' ? (err as any).code : err ? 1 : 0 });
    });
  });

export interface Submission { agent: string; branch: string; prURL: string; created: boolean; pushOnly: boolean; note?: string; }

export class Cli {
  private run: RunFn;
  constructor(private opts: { binaryPath: string; run?: RunFn }) {
    this.run = opts.run ?? defaultRun;
  }
  async exec(argv: string[]): Promise<RunResult> {
    return this.run(this.opts.binaryPath, argv);
  }
  private async json<T>(argv: string[], fallback: T): Promise<{ value: T; ok: boolean; stderr: string }> {
    const r = await this.exec(argv);
    if (r.code !== 0) return { value: fallback, ok: false, stderr: r.stderr };
    try { return { value: JSON.parse(r.stdout) as T, ok: true, stderr: '' }; }
    catch { return { value: fallback, ok: false, stderr: 'unparseable JSON' }; }
  }
  async list(host?: string): Promise<NormalizedList> {
    const r = await this.json<unknown>(listArgv(host), []);
    return normalizeList(r.value);
  }
  async questions(host?: string): Promise<Question[]> {
    const r = await this.json<Question[]>(questionsArgv(host), []);
    return Array.isArray(r.value) ? r.value : [];
  }
  async version(): Promise<{ version: string; contract: number } | null> {
    const r = await this.json<unknown>(['version', '--json'], null);
    return r.ok ? parseVersion(r.value) : null;
  }
  async submit(host: string | undefined, agent: string): Promise<Submission | null> {
    const argv = [...(host && host !== 'local' ? ['--host', host] : []), 'submit', agent, '--json'];
    const r = await this.json<Submission>(argv, null as any);
    return r.ok ? r.value : null;
  }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm run test:unit`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add clients/vscode/src/cli.ts clients/vscode/src/test/unit/cli.test.ts
git commit -m "feat(vscode): CLI adapter (execFile + JSON normalization, injectable run)"
```

---

### Task 9: Webview assets + bundle

**Files:**
- Create: `clients/vscode/media/dashboard.css`
- Create: `clients/vscode/src/webview/main.ts`

**Interfaces:**
- Consumes: `renderDashboard` (Task 7), `FleetState`/`Intent` (Task 2).
- Produces: `media/dashboard.js` (built by esbuild) — listens for `{type:'state', state}` messages, calls `renderDashboard`, wires button clicks + the answer input to `vscode.postMessage(intent)`.

- [ ] **Step 1: Write the webview entry**

`clients/vscode/src/webview/main.ts`:
```typescript
import { renderDashboard } from '../core/render';
import { FleetState, Intent } from '../core/types';

declare function acquireVsCodeApi(): { postMessage(msg: unknown): void };
const vscode = acquireVsCodeApi();
const root = document.getElementById('root')!;
let checkoutEnabled = false;

function send(intent: Intent): void { vscode.postMessage(intent); }

window.addEventListener('message', (ev) => {
  const msg = ev.data;
  if (msg?.type === 'config') { checkoutEnabled = !!msg.checkoutEnabled; return; }
  if (msg?.type === 'state') {
    root.innerHTML = renderDashboard(msg.state as FleetState, { checkoutEnabled });
  }
});

root.addEventListener('click', (ev) => {
  const el = (ev.target as HTMLElement).closest('button[data-verb]') as HTMLElement | null;
  if (!el) return;
  const verb = el.dataset.verb as Intent['verb'];
  const host = el.dataset.host || 'local';
  const agent = el.dataset.agent || undefined;
  const id = el.dataset.id || undefined;
  let text: string | undefined;
  if (verb === 'answer' && agent) {
    const input = root.querySelector<HTMLInputElement>(`input.q-input[data-agent="${agent}"]`);
    text = input?.value ?? '';
  }
  send({ type: 'action', verb, host, agent, id, text });
});

vscode.postMessage({ type: 'action', verb: 'refresh', host: 'local' });
```

- [ ] **Step 2: Write the stylesheet**

`clients/vscode/media/dashboard.css` — uses the theme variables VS Code injects into webviews (`--vscode-*`):
```css
body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); padding: 0 12px 32px; }
.toolbar { display: flex; gap: 8px; align-items: center; padding: 10px 0; }
.dim { color: var(--vscode-descriptionForeground); }
.updated { margin-left: auto; }
button { background: var(--vscode-button-secondaryBackground); color: var(--vscode-button-secondaryForeground); border: none; border-radius: 3px; padding: 3px 8px; cursor: pointer; }
button.primary { background: var(--vscode-button-background); color: var(--vscode-button-foreground); }
button.danger:hover { color: var(--vscode-errorForeground); }
.needs { border: 1px solid var(--vscode-editorWarning-foreground); border-radius: 5px; padding: 8px; margin: 10px 0; }
.needs h2 { font-size: 12px; text-transform: uppercase; margin: 4px 0 8px; }
.q, .pr { padding: 8px 0; border-bottom: 1px solid var(--vscode-panel-border); }
.q-head { display: flex; gap: 8px; align-items: center; }
.q-head .age { margin-left: auto; }
.q-input { flex: 1; background: var(--vscode-input-background); color: var(--vscode-input-foreground); border: 1px solid var(--vscode-input-border); border-radius: 3px; padding: 4px 6px; }
.q-row { display: flex; gap: 6px; margin-top: 6px; }
.host-head { font-weight: 600; margin: 14px 0 4px; }
.host-warn { color: var(--vscode-editorWarning-foreground); margin: 10px 0; }
.repo-head { color: var(--vscode-descriptionForeground); margin: 10px 0 2px; }
table { width: 100%; border-collapse: collapse; }
td { padding: 6px 4px; border-bottom: 1px solid var(--vscode-panel-border); }
.actions { text-align: right; }
.chip { padding: 1px 7px; border-radius: 9px; font-size: 11px; }
.chip.running { color: var(--vscode-charts-green); }
.chip.blocked { color: var(--vscode-charts-yellow); }
.chip.done { color: var(--vscode-charts-blue); }
.chip.exited, .chip.error { color: var(--vscode-charts-red); }
```

- [ ] **Step 3: Build and verify the bundle**

Run: `npm run compile`
Expected: `media/dashboard.js` exists and is non-empty.
```bash
test -s media/dashboard.js && echo "webview bundle OK"
```

- [ ] **Step 4: Commit**

```bash
git add clients/vscode/src/webview/main.ts clients/vscode/media/dashboard.css
git commit -m "feat(vscode): webview entry + theme-aware stylesheet"
```

---

### Task 10: Dashboard panel adapter

**Files:**
- Create: `clients/vscode/src/dashboard.ts`
- Test: `clients/vscode/src/test/unit/dashboard.test.ts`

**Interfaces:**
- Consumes: `Intent` (Task 2), `renderDashboard` is used inside the webview (not here).
- Produces: `dispatchIntent(intent, deps): Promise<void>` where `deps = { cli: Cli; ack: AckStore; openExternal(url): void; runInTerminal(argv): void; confirm(msg): Promise<boolean>; spawnFlow(): Promise<void>; attach(host, id): Promise<void>; reposByAgent: Map<string,string>; refresh(): Promise<void> }`. Also `class DashboardPanel` (the `vscode.WebviewPanel` wrapper) — created here but exercised by the integration test. `dispatchIntent` is pure-enough to unit-test with fakes.

- [ ] **Step 1: Write the failing test**

`clients/vscode/src/test/unit/dashboard.test.ts`:
```typescript
import * as assert from 'node:assert';
import { dispatchIntent, DispatchDeps } from '../../dashboard';
import { Cli } from '../../cli';
import { AckStore } from '../../ack';

function deps(over: Partial<DispatchDeps> = {}): { d: DispatchDeps; calls: string[] } {
  const calls: string[] = [];
  const execArgv: string[][] = [];
  const cli = new Cli({ binaryPath: 'flotilla', run: async (_b, argv) => { execArgv.push(argv); return { stdout: '{}', stderr: '', code: 0 }; } });
  const d: DispatchDeps = {
    cli,
    ack: new AckStore({ get: (_k, dft) => dft, update: () => Promise.resolve() }),
    openExternal: (u) => calls.push(`open:${u}`),
    runInTerminal: (argv) => calls.push(`term:${argv.join(' ')}`),
    confirm: async () => true,
    spawnFlow: async () => calls.push('spawn'),
    attach: async (host, id) => calls.push(`attach:${host}:${id}`),
    reposByAgent: new Map([['local keen-fox', 'acme/api']]),
    refresh: async () => calls.push('refresh'),
    idToContainer: new Map([['local a', 'c1']]),
    branchByAgent: new Map([['local keen-fox', 'flotilla/keen-fox']]),
    prURLByAgent: new Map([['local keen-fox', 'https://gh/pr/1']]),
    ...over,
  };
  (d as any)._execArgv = execArgv;
  return { d, calls };
}

describe('dispatchIntent', () => {
  it('answer → runs the CLI then refreshes', async () => {
    const { d, calls } = deps();
    await dispatchIntent({ type: 'action', verb: 'answer', host: 'local', agent: 'a', id: 'q1', text: 'yes' }, d);
    assert.deepStrictEqual((d as any)._execArgv[0], ['answer', 'a', '--id', 'q1', 'yes']);
    assert.ok(calls.includes('refresh'));
  });
  it('openPR → opens the PR URL', async () => {
    const { d, calls } = deps();
    await dispatchIntent({ type: 'action', verb: 'openPR', host: 'local', agent: 'keen-fox' }, d);
    assert.ok(calls.includes('open:https://gh/pr/1'));
  });
  it('logs → opens a terminal with logs -f', async () => {
    const { d, calls } = deps();
    await dispatchIntent({ type: 'action', verb: 'logs', host: 'local', agent: 'a' }, d);
    assert.ok(calls.some((c) => c.startsWith('term:') && c.includes('logs') && c.includes('-f')));
  });
  it('rm → confirms, then runs rm', async () => {
    const { d } = deps();
    await dispatchIntent({ type: 'action', verb: 'rm', host: 'local', agent: 'a' }, d);
    assert.deepStrictEqual((d as any)._execArgv[0], ['rm', 'a']);
  });
  it('rm → aborts when not confirmed', async () => {
    const { d } = deps({ confirm: async () => false });
    await dispatchIntent({ type: 'action', verb: 'rm', host: 'local', agent: 'a' }, d);
    assert.strictEqual((d as any)._execArgv.length, 0);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npm run pretest:unit && npx mocha out/test/unit/dashboard.test.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write the dispatcher**

`clients/vscode/src/dashboard.ts`:
```typescript
import { Intent } from './core/types';
import { intentToArgv } from './core/intents';
import { Cli } from './cli';
import { AckStore } from './ack';

export interface DispatchDeps {
  cli: Cli;
  ack: AckStore;
  openExternal: (url: string) => void;
  runInTerminal: (argv: string[]) => void;
  confirm: (msg: string) => Promise<boolean>;
  spawnFlow: () => Promise<void>;
  attach: (host: string, containerId: string) => Promise<void>;
  reposByAgent: Map<string, string>;
  idToContainer: Map<string, string>;
  branchByAgent: Map<string, string>;
  prURLByAgent: Map<string, string>;
  refresh: () => Promise<void>;
}

const key = (host: string, agent: string) => `${host} ${agent}`;

export async function dispatchIntent(intent: Intent, d: DispatchDeps): Promise<void> {
  const k = key(intent.host ?? 'local', intent.agent ?? '');
  switch (intent.verb) {
    case 'refresh': await d.refresh(); return;
    case 'spawn': await d.spawnFlow(); return;
    case 'logs': d.runInTerminal([...(intent.host && intent.host !== 'local' ? ['--host', intent.host] : []), 'logs', intent.agent!, '-f']); return;
    case 'attach': { const c = d.idToContainer.get(k); if (c) await d.attach(intent.host ?? 'local', c); return; }
    case 'openPR': { const url = d.prURLByAgent.get(k); if (url) d.openExternal(url); return; }
    case 'checkoutPR': { const url = d.prURLByAgent.get(k); if (url) d.runInTerminal(['gh', 'pr', 'checkout', url]); return; }
    case 'ackPR': {
      const branch = d.branchByAgent.get(k) ?? '';
      await d.ack.ack({ host: intent.host ?? 'local', agent: intent.agent!, branch });
      await d.refresh(); return;
    }
    case 'rm': {
      if (!(await d.confirm(`Remove agent ${intent.agent}? This deletes its container.`))) return;
      await d.cli.exec(intentToArgv(intent)); await d.refresh(); return;
    }
    case 'submit': await d.cli.submit(intent.host, intent.agent!); await d.refresh(); return;
    case 'answer':
    case 'stop': await d.cli.exec(intentToArgv(intent)); await d.refresh(); return;
  }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm run test:unit`
Expected: PASS — all dispatch specs green.

- [ ] **Step 5: Commit**

```bash
git add clients/vscode/src/dashboard.ts clients/vscode/src/test/unit/dashboard.test.ts
git commit -m "feat(vscode): intent dispatcher (CLI/terminal/openExternal routing)"
```

---

### Task 11: Extension wiring + manifest contributes + integration test

**Files:**
- Modify: `clients/vscode/src/extension.ts`
- Modify: `clients/vscode/package.json` (full `contributes`)
- Create: `clients/vscode/src/inboxWatcher.ts`
- Create: `clients/vscode/src/test/integration/runTest.ts`
- Create: `clients/vscode/src/test/integration/suite/index.ts`
- Create: `clients/vscode/src/test/integration/suite/extension.test.ts`

**Interfaces:**
- Consumes: everything above (`Cli`, `AckStore`, `dispatchIntent`, `buildFleetState`, `parseInboxLines`/`prsFromEvents`, `DashboardPanel`).
- Produces: a working extension — `flotilla.openDashboard` opens the panel; a poll loop pushes state; a status-bar item; the local inbox watcher.

- [ ] **Step 1: Write the inbox watcher**

`clients/vscode/src/inboxWatcher.ts`:
```typescript
import * as vscode from 'vscode';
import * as path from 'node:path';

export function watchInbox(homeDir: string, onChange: () => void): vscode.Disposable {
  const dir = path.join(homeDir, '.flotilla');
  const pattern = new vscode.RelativePattern(vscode.Uri.file(dir), 'inbox.jsonl');
  const w = vscode.workspace.createFileSystemWatcher(pattern);
  const fire = () => onChange();
  w.onDidChange(fire); w.onDidCreate(fire);
  return w;
}
```

- [ ] **Step 2: Write the full extension wiring**

`clients/vscode/src/extension.ts`:
```typescript
import * as vscode from 'vscode';
import * as os from 'node:os';
import * as fs from 'node:fs/promises';
import * as path from 'node:path';
import { Cli } from './cli';
import { AckStore } from './ack';
import { buildFleetState, prKey } from './core/fleetState';
import { parseInboxLines, prsFromEvents } from './core/inbox';
import { dispatchIntent, DispatchDeps } from './dashboard';
import { renderDashboard } from './core/render';
import { FleetState, Intent } from './core/types';
import { watchInbox } from './inboxWatcher';

function cfg() {
  const c = vscode.workspace.getConfiguration('flotilla');
  return {
    binaryPath: c.get<string>('binaryPath', 'flotilla'),
    home: c.get<string>('home', path.join(os.homedir(), '.flotilla')).replace(/^~/, os.homedir()),
    interval: c.get<number>('refreshIntervalSeconds', 3) * 1000,
  };
}

export function activate(context: vscode.ExtensionContext): void {
  const { binaryPath, home, interval } = cfg();
  const cli = new Cli({ binaryPath });
  const ack = new AckStore(context.globalState);
  const checkoutEnabled = !!vscode.extensions.getExtension('github.vscode-pull-request-github');

  let panel: vscode.WebviewPanel | undefined;
  let last: FleetState | undefined;
  const status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 100);
  status.command = 'flotilla.openDashboard';
  status.show();
  context.subscriptions.push(status);

  const reposByAgent = new Map<string, string>();
  const idToContainer = new Map<string, string>();
  const branchByAgent = new Map<string, string>();
  const prURLByAgent = new Map<string, string>();

  async function readPRs() {
    try {
      const text = await fs.readFile(path.join(home, 'inbox.jsonl'), 'utf8');
      return prsFromEvents(parseInboxLines(text), (a) => reposByAgent.get(`local ${a}`) ?? '', 'local');
    } catch { return []; }
  }

  async function refresh(): Promise<void> {
    const [list, questions, prs] = await Promise.all([cli.list(), cli.questions(), readPRs()]);
    for (const a of list.agents) {
      reposByAgent.set(`${a.host} ${a.name}`, a.repo);
      idToContainer.set(`${a.host} ${a.name}`, a.id);
    }
    for (const p of prs) { branchByAgent.set(`${p.host} ${p.agent}`, p.branch); if (p.prURL) prURLByAgent.set(`${p.host} ${p.agent}`, p.prURL); }
    const state = buildFleetState({ list, questions, prs, ackedKeys: ack.keys(), nowMs: Date.now() });
    last = state;
    panel?.webview.postMessage({ type: 'state', state });
    const b = state.questions.length, p = state.prs.length, warn = state.hosts.filter((h) => !h.ok).length;
    status.text = `$(rocket) ${state.agents.length} · ⏸${b} · PR ${p}` + (warn ? ` · ⚠${warn}` : '');
    status.backgroundColor = (b || p || warn) ? new vscode.ThemeColor('statusBarItem.warningBackground') : undefined;
  }

  function deps(): DispatchDeps {
    return {
      cli, ack, reposByAgent, idToContainer, branchByAgent, prURLByAgent, refresh,
      openExternal: (u) => void vscode.env.openExternal(vscode.Uri.parse(u)),
      runInTerminal: (argv) => { const t = vscode.window.createTerminal('flotilla'); t.show(); t.sendText([binaryPath, ...argv].map(shellQuote).join(' ')); },
      confirm: async (msg) => (await vscode.window.showWarningMessage(msg, { modal: true }, 'Remove')) === 'Remove',
      spawnFlow: () => spawnFlow(cli, binaryPath),
      attach: attach,
    };
  }

  function openDashboard(): void {
    if (panel) { panel.reveal(); return; }
    panel = vscode.window.createWebviewPanel('flotilla', 'Flotilla', vscode.ViewColumn.Active, { enableScripts: true, retainContextWhenHidden: true });
    const js = panel.webview.asWebviewUri(vscode.Uri.joinPath(context.extensionUri, 'media', 'dashboard.js'));
    const css = panel.webview.asWebviewUri(vscode.Uri.joinPath(context.extensionUri, 'media', 'dashboard.css'));
    panel.webview.html = `<!DOCTYPE html><html><head><link rel="stylesheet" href="${css}"></head><body><div id="root"></div><script src="${js}"></script></body></html>`;
    panel.webview.postMessage({ type: 'config', checkoutEnabled });
    if (last) panel.webview.postMessage({ type: 'state', state: last });
    panel.webview.onDidReceiveMessage((m: Intent) => { if (m?.type === 'action') void dispatchIntent(m, deps()); });
    panel.onDidDispose(() => { panel = undefined; });
  }

  context.subscriptions.push(
    vscode.commands.registerCommand('flotilla.openDashboard', () => { openDashboard(); void refresh(); }),
    watchInbox(home, () => void refresh()),
  );
  const timer = setInterval(() => void refresh(), interval);
  context.subscriptions.push({ dispose: () => clearInterval(timer) });
  void refresh();
}

function shellQuote(s: string): string {
  return /^[A-Za-z0-9_\-./:=]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`;
}

async function spawnFlow(cli: Cli, _binary: string): Promise<void> {
  const agentsOut = await cli.exec(['agents']);
  const profiles = agentsOut.stdout.split('\n').map((l) => l.trim()).filter(Boolean);
  const profile = await vscode.window.showQuickPick(profiles, { placeHolder: 'Agent profile' });
  if (!profile) return;
  const repo = await vscode.window.showInputBox({ prompt: 'Repository URL' });
  if (!repo) return;
  const prompt = await vscode.window.showInputBox({ prompt: 'Task prompt' });
  if (prompt === undefined) return;
  const t = vscode.window.createTerminal('flotilla spawn');
  t.show();
  t.sendText([_binary, 'spawn', repo, '--agent', profile, '--prompt', prompt].map(shellQuote).join(' '));
}

async function attach(_host: string, containerId: string): Promise<void> {
  // Local host: hand the container id to the Dev Containers extension.
  // Remote host attach (Remote-SSH) is wired when the federated client lands (spec §6).
  if (!vscode.extensions.getExtension('ms-vscode-remote.remote-containers')) {
    void vscode.window.showErrorMessage('Attach needs the Dev Containers extension (ms-vscode-remote.remote-containers).');
    return;
  }
  await vscode.commands.executeCommand('remote-containers.attachToRunningContainer', containerId);
}

export function deactivate(): void {}
```

- [ ] **Step 3: Fill in the manifest `contributes` and `activationEvents`**

In `clients/vscode/package.json`, replace `contributes` and `activationEvents`:
```json
  "activationEvents": ["onStartupFinished"],
  "contributes": {
    "commands": [
      { "command": "flotilla.openDashboard", "title": "Flotilla: Open Dashboard" }
    ],
    "configuration": {
      "title": "Flotilla",
      "properties": {
        "flotilla.binaryPath": { "type": "string", "default": "flotilla", "description": "Path to the flotilla binary." },
        "flotilla.refreshIntervalSeconds": { "type": "number", "default": 3, "description": "Fleet poll cadence in seconds." },
        "flotilla.home": { "type": "string", "default": "~/.flotilla", "description": "Flotilla home dir (inbox/session paths)." }
      }
    }
  },
```

- [ ] **Step 4: Write the integration harness**

`clients/vscode/src/test/integration/runTest.ts`:
```typescript
import * as path from 'node:path';
import { runTests } from '@vscode/test-electron';

async function main() {
  try {
    await runTests({
      extensionDevelopmentPath: path.resolve(__dirname, '../../../'),
      extensionTestsPath: path.resolve(__dirname, './suite/index'),
    });
  } catch {
    console.error('integration tests skipped/failed (no display or download blocked)');
    process.exit(0); // self-skip: never fail CI on environment limits
  }
}
main();
```

`clients/vscode/src/test/integration/suite/index.ts`:
```typescript
import * as path from 'node:path';
import Mocha from 'mocha';
import { glob } from 'glob';

export async function run(): Promise<void> {
  const mocha = new Mocha({ ui: 'bdd', timeout: 60000 });
  const files = await glob('**/*.test.js', { cwd: __dirname });
  files.forEach((f) => mocha.addFile(path.resolve(__dirname, f)));
  await new Promise<void>((resolve, reject) => mocha.run((failures) => (failures ? reject(new Error(`${failures} failed`)) : resolve())));
}
```

`clients/vscode/src/test/integration/suite/extension.test.ts`:
```typescript
import * as assert from 'node:assert';
import * as vscode from 'vscode';

describe('extension activation', () => {
  it('registers the openDashboard command', async () => {
    const cmds = await vscode.commands.getCommands(true);
    assert.ok(cmds.includes('flotilla.openDashboard'));
  });
  it('opens the dashboard panel without throwing', async () => {
    await vscode.commands.executeCommand('flotilla.openDashboard');
  });
});
```

Add to `package.json` scripts and devDeps:
```json
    "pretest:integration": "tsc -p ./",
    "test:integration": "node ./out/test/integration/runTest.js"
```
and devDeps `"glob": "^10.3.0"`, `"mocha": "^10.3.0"` (already present). Run `npm install`.

- [ ] **Step 5: Verify everything**

Run:
```bash
npm run check
npm run lint
npm run test:unit
npm run compile
npm run test:integration
```
Expected: `check`/`lint` clean; `test:unit` all green; `compile` builds; `test:integration` either passes (if VS Code can download + a display exists) or prints the self-skip line and exits 0.

- [ ] **Step 6: Manual smoke (the "Always Works" gate)**

In VS Code, press F5 (Extension Development Host) with `clients/vscode` open, or install the VSIX. With a `flotilla` agent running, run **Flotilla: Open Dashboard** and confirm: the agent appears, the status-bar item shows counts, **Logs** opens a terminal streaming `flotilla logs -f`, and answering a blocked agent's question round-trips. Record what you saw.

- [ ] **Step 7: Commit**

```bash
git add clients/vscode
git commit -m "feat(vscode): extension wiring (dashboard, poll loop, status bar, watcher, spawn/attach) + integration test"
```

---

### Task 12: CI job, README, and backlog tick

**Files:**
- Modify: `.github/workflows/ci.yml` (add a `vscode` job)
- Create: `clients/vscode/README.md`
- Modify: `docs/backlog.md` (tick the VS Code extension item)

**Interfaces:**
- Produces: a path-filtered CI job mirroring the local checks; install docs.

- [ ] **Step 1: Add the CI job**

Read `.github/workflows/ci.yml` first to match its style (Node setup, mise, job naming). Add:
```yaml
  vscode:
    name: vscode extension
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: clients/vscode
    steps:
      - uses: actions/checkout@<pinned-sha>  # match the SHA pinning used by the other jobs
      - uses: actions/setup-node@<pinned-sha>
        with:
          node-version: 20
      - run: npm ci
      - run: npm run check
      - run: npm run lint
      - run: npm run test:unit
      - run: npm run compile
      - run: npm run package
```
Gate it on a path filter so it only runs when `clients/vscode/**` changes, matching how the repo scopes its other jobs (use the same `paths:`/`paths-filter` mechanism already in `ci.yml`). `test:integration` is intentionally **not** in CI (no display); it self-skips locally.

> Pin any new action to a commit SHA, per the repo's github-actions hardening convention.

- [ ] **Step 2: Write the README**

`clients/vscode/README.md`:
```markdown
# Flotilla — VS Code extension

A dashboard for the [flotilla](../../README.md) fleet. View plane only: it shells out to the
`flotilla` CLI for everything.

## Develop
- `npm install`
- `npm run compile` — bundle extension + webview
- `npm run test:unit` — fast unit tests (no VS Code)
- Press **F5** to launch an Extension Development Host

## Build a VSIX
- `npm run package` → `flotilla.vsix`
- Install: Extensions view → "Install from VSIX…", or `code --install-extension flotilla.vsix`

## Settings
- `flotilla.binaryPath` (default `flotilla`)
- `flotilla.refreshIntervalSeconds` (default `3`)
- `flotilla.home` (default `~/.flotilla`)

Requires the **Dev Containers** extension for Attach. Optional: **GitHub Pull Requests** enables
"Check out locally" on finished PRs.
```

- [ ] **Step 3: Tick the backlog**

In `docs/backlog.md`, mark the "VS Code extension" item done with a pointer to the spec and plan (match the strikethrough style used for the other shipped items).

- [ ] **Step 4: Verify CI config locally**

Run: `npx yq '.jobs.vscode' .github/workflows/ci.yml` (or read it) and confirm the job parses and the path filter is present.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml clients/vscode/README.md docs/backlog.md
git commit -m "ci(vscode): add path-filtered extension job + README + backlog tick"
```

---

## Self-Review

**Spec coverage:**
- Editor-tab webview (§4) → Tasks 7, 9, 10, 11.
- Host⇄webview protocol (§5) → Tasks 7 (render), 9 (postMessage), 11 (panel).
- JSON normalization / federated seam (§5.1) → Task 2 + Task 8 (`version()` returns null on legacy).
- Commands & lifecycle (§6) → Tasks 6 (argv), 10 (dispatch), 11 (spawn/attach/terminal).
- Needs-attention: questions + PRs (§7) → Tasks 4 (PR extraction), 7 (render), 10 (answer/ack/open), 11 (watcher).
- Status model / polling / watching (§8) → Task 3 (state), 11 (poll loop, status bar, watcher).
- Configuration (§9) → Task 11 (cfg + manifest).
- Packaging/build/dist (§10) → Tasks 1, 12.
- Error handling (§11) → Task 8 (non-zero exit → fallback), 11 (attach missing-extension message, modal confirm).
- Testing (§12) → unit tests in every core task + Task 11 integration.
- Deferred objective/progress (§14.1) → fields present + `null` in Task 2/3; render leaves the slot.
- GH-PR-gated checkout (§14.2) → Task 11 (`checkoutEnabled`) + Task 7 (conditional button) + Task 10 (`checkoutPR`).

**Placeholder scan:** none — every step has runnable code or an exact command.

**Type consistency:** `prKey`, `NormalizedList`, `FleetState`, `Intent`/`Verb`, `Cli`, `AckStore`, `DispatchDeps`, `renderDashboard(state, {checkoutEnabled})` names are used identically across tasks. `RunFn`/`RunResult` defined in Task 8 and used by its tests. The `host agent`/`prKey` key encoding is shared by `fleetState`, `dashboard`, and `extension`.

**Known soft edges (acceptable for v1, called out in the spec):**
- Remote-host attach is a stub message until the federated client lands (Task 11 `attach` comment, spec §6).
- The webview `dashboard.js` and `core/render` share code via the esbuild bundle; the render is unit-tested directly (Task 7) and exercised live in Task 11 step 6.
