// Failures-only reporter for `node --test` (Node's default prints a TAP block per passing test in CI).
// Prints the output of files that failed, each failure's stack, then the run summary.
// Selected by `make plan-review-check` unless V=1; RUNNER_DEBUG=1 prints Node's full spec output.
import { realpathSync, writeSync } from 'node:fs';
import path from 'node:path';
import { Readable } from 'node:stream';
import { spec } from 'node:test/reporters';

const ROOT = realpathSync(process.cwd());

// frames inside Node itself (node:internal/..., node:async_hooks) or anonymous builtins never locate a bug
const NODE_FRAME = /^\s*at (?:.*\((?:node:|<anonymous>\))|node:)/;

// output events carry the file as given on the command line, failures carry its real path
const relative = (file) => (file ? path.relative(ROOT, path.resolve(ROOT, file)) : '?');

function formatError(error) {
  // the runner wraps the thrown value in ERR_TEST_FAILURE; the cause is what the test threw
  const thrown = error?.cause ?? error;
  const text = typeof thrown === 'string' ? thrown : (thrown?.stack ?? String(thrown));
  return text
    .split('\n')
    .filter((line) => !NODE_FRAME.test(line))
    .map((line) => `  ${line.replaceAll(`file://${ROOT}/`, '').replaceAll(`${ROOT}/`, '')}`)
    .join('\n');
}

// the output of the given files, then every failure
function formatFailures(files, outputByFile, failures) {
  let text = '';
  for (const file of files) {
    const output = outputByFile.get(file);
    if (output) text += `---- output of ${file} ----\n${output}---- end output ----\n\n`;
  }
  for (const failure of failures) {
    text += `✖ ${failure.name} (${relative(failure.file)}:${failure.line})\n${formatError(failure.details?.error)}\n\n`;
  }
  return text;
}

export default async function* failuresOnly(source) {
  if (process.env.RUNNER_DEBUG === '1') {
    yield* Readable.from(source).pipe(new spec());
    return;
  }
  const outputByFile = new Map();
  const failures = [];
  const summary = [];
  // tests that started and have not completed: the suspects when a run is cut short
  const running = new Map();
  let finished = false;

  // This reporter writes once the run ends, so a run killed part way (a CI job timeout, Ctrl-C)
  // would print nothing at all. The runner calls process.exit() synchronously on SIGINT/SIGTERM,
  // so say what is known from the exit hook: what was still running and the failures so far.
  process.once('exit', () => {
    if (finished) return;
    const stuck = [...running.values()].map((test) => `  ${test.name} (${relative(test.file)}:${test.line ?? 1})\n`);
    const files = new Set([...running.values(), ...failures].map((test) => relative(test.file)));
    const text = `✖ test run interrupted before it finished; still running:\n${stuck.join('')}\n${formatFailures(files, outputByFile, failures)}`;
    try {
      writeSync(process.stdout.fd, text);
    } catch {
      // stdout is gone; nothing left to tell
    }
  });

  for await (const { type, data } of source) {
    const key = `${data?.file}:${data?.line}:${data?.column}:${data?.nesting}:${data?.name}`;
    if (type === 'test:dequeue') {
      running.set(key, data);
    } else if (type === 'test:complete') {
      running.delete(key);
    } else if (type === 'test:stdout' || type === 'test:stderr') {
      const file = relative(data.file);
      outputByFile.set(file, (outputByFile.get(file) ?? '') + data.message);
    } else if (type === 'test:fail') {
      // a suite failing only because a child failed repeats that child's failure
      if (data.details?.error?.failureType !== 'subtestsFailed') failures.push(data);
    } else if (type === 'test:diagnostic' && data.nesting === 0) {
      summary.push(`ℹ ${data.message}\n`);
    }
  }

  finished = true;
  const files = new Set(failures.map((failure) => relative(failure.file)));
  const report = formatFailures(files, outputByFile, failures);
  if (report) yield report;
  yield* summary;
}
