import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const source = readFileSync(new URL('../src/chatUtils.ts', import.meta.url), 'utf8');
const js = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
}).outputText;
const {
  canonicalizeTranscriptItems,
  findAssistantIndexForToolResult,
  pickTurnSummaryIndex,
  resultError,
  transcriptSignature,
} = await import(`data:text/javascript;base64,${Buffer.from(js).toString('base64')}`);

const row = {
  kind: 'msg',
  key: '42',
  role: 'assistant',
  content: 'before',
  toolCalls: [{ name: 'read_file', detail: '{"path":"before"}' }],
  toolResults: [{ name: 'read_file', detail: 'old result' }],
  usage: { input: 10, output: 4 },
};
assert.notEqual(
  transcriptSignature([row]),
  transcriptSignature([{ ...row, content: 'after!' }]),
  'same-length content edits must invalidate the transcript',
);
assert.notEqual(
  transcriptSignature([row]),
  transcriptSignature([{ ...row, toolResults: [{ name: 'read_file', detail: 'new result' }] }]),
  'same-arity tool result changes must invalidate the transcript',
);
assert.notEqual(
  transcriptSignature([row]),
  transcriptSignature([{ ...row, usage: { input: 11, output: 4 } }]),
  'usage values, not only usage presence, belong in the signature',
);
assert.equal(
  transcriptSignature([{ ...row, streaming: true, editing: true }]),
  transcriptSignature([{ ...row, streaming: false, stale: true }]),
  'ephemeral rendering flags do not invalidate an unchanged row',
);
const attachment = { name: 'pixel.png', url: 'data:image/png;base64,AAAA' };
assert.notEqual(
  transcriptSignature([{ ...row, attachments: [attachment] }]),
  transcriptSignature([{ ...row, attachments: [{ ...attachment, url: 'data:image/png;base64,BBBB' }] }]),
  'same-size inline image replacements must invalidate the transcript',
);

assert.equal(
  resultError('{"error":{"message":"Internal","innerText":"add_to_list tool not found"}}'),
  'add_to_list tool not found',
);
assert.equal(resultError('{"error":{"message":"Useful failure"}}'), 'Useful failure');
assert.equal(resultError('{"error":"Direct failure"}'), 'Direct failure');
assert.equal(resultError('ordinary successful text'), null);
assert.equal(resultError('{"ok":true}'), null);

// Synthetic live rows must stay anchored at their chronological `afterId`
// instead of sinking below later numeric injected/follow-up rows.
const mixed = [
  { key: 'u1', afterId: 0 },
  { key: 's2', afterId: 0 },
  { key: 't3', afterId: 0 },
  { key: '15' },
  { key: 's4', afterId: 15 },
];
assert.deepEqual(
  canonicalizeTranscriptItems(mixed).map((it) => it.key),
  ['u1', 's2', 't3', '15', 's4'],
  'mid-turn numeric injected rows must not jump above earlier synthetic rows',
);

const outOfOrderPersisted = [
  { key: '10' },
  { key: '14' },
  { key: '12' },
  { key: 's5', afterId: 10 },
];
assert.deepEqual(
  canonicalizeTranscriptItems(outOfOrderPersisted).map((it) => it.key),
  ['10', 's5', '12', '14'],
  'synthetic rows anchored after id 10 must stay between 10 and 12',
);

// Tool results persisted after an injected screenshot/background/steered user
// row must pair with the owning assistant row instead of creating an orphan row
// below the injection.
const mappedWithInjection = [
  { kind: 'msg', key: '1', role: 'user' },
  {
    kind: 'msg',
    key: '2',
    role: 'assistant',
    toolCalls: [{ id: 'call_shot' }, { id: 'call_read' }],
  },
  { kind: 'msg', key: '3', role: 'user', injected: true },
];
assert.equal(
  findAssistantIndexForToolResult(mappedWithInjection, 'call_read'),
  1,
  'tool result after an injected screenshot row must pair with its owning assistant row',
);
assert.equal(
  findAssistantIndexForToolResult(mappedWithInjection),
  1,
  'tool result without call id still crosses intervening injected user rows',
);
assert.equal(
  findAssistantIndexForToolResult([
    ...mappedWithInjection,
    { kind: 'msg', key: '4', role: 'user' },
  ]),
  -1,
  'a real user prompt closes tool-result pairing without a matching call id',
);

// Pre-tool assistant comments must stay inside the work group rather than
// being promoted below subsequent tool calls.
assert.equal(
  pickTurnSummaryIndex([
    {
      it: { kind: 'msg', role: 'assistant', content: 'Let me check the files first.' },
      d: { calls: [], results: [] },
    },
    {
      it: { kind: 'tool', name: 'read_file' },
      d: { calls: [{ name: 'read_file', detail: '{}' }], results: [] },
    },
  ]),
  -1,
  'pre-tool assistant text followed by tools must not be pulled below the tools',
);
assert.equal(
  pickTurnSummaryIndex([
    {
      it: { kind: 'msg', role: 'assistant', content: 'Taking a screenshot now.' },
      d: { calls: [{ name: 'screenshot_app', detail: '{}' }], results: [] },
    },
  ]),
  -1,
  'an assistant row carrying its own tool calls is part of the work group, not a post-tool summary',
);
assert.equal(
  pickTurnSummaryIndex([
    {
      it: { kind: 'tool', name: 'read_file' },
      d: { calls: [{ name: 'read_file', detail: '{}' }], results: [] },
    },
    {
      it: { kind: 'msg', role: 'assistant', content: 'All done.' },
      d: { calls: [], results: [] },
    },
  ]),
  1,
  'a pure final assistant text row after tools is the turn summary',
);
assert.equal(
  pickTurnSummaryIndex([
    {
      it: { kind: 'msg', role: 'assistant', content: '' },
      d: {
        calls: [{ name: 'compress', detail: '{}' }],
        results: [{ name: 'compress', detail: 'Folded context: m00649–m00672' }],
      },
    },
    {
      it: {
        kind: 'msg',
        role: 'assistant',
        content: 'Both shipped as `21a40a8` on top of v0.1.98.',
        usage: { input: 137882, output: 750, context: 138632 },
      },
      d: { calls: [], results: [] },
    },
    {
      it: { kind: 'tool', name: 'edit_file' },
      d: { calls: [{ name: 'edit_file', detail: '{"path":"web/fields.tsx"}' }], results: [] },
    },
  ]),
  1,
  'a usage-bearing turn-final assistant summary must never be folded inside CollapsedToolsGroup between Folded context and a trailing synthetic tool card',
);

console.log('chat utility regression tests passed');
