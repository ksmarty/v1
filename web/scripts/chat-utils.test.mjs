import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const source = readFileSync(new URL('../src/chatUtils.ts', import.meta.url), 'utf8');
const js = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
}).outputText;
const { resultError, transcriptSignature } = await import(
  `data:text/javascript;base64,${Buffer.from(js).toString('base64')}`
);

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

console.log('chat utility regression tests passed');
